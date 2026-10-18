{
  buildGoModule,
  git,
  gnugrep,
  jq,
  lib,
  openssh,
  stdenv,
  # wgn.home.llm.guard.credentialPaths, a path under home as ~/…, which the
  # guard falls back on where its config is absent.
  credentialPaths ? [],
  # The host's XDG config and state directories, which the programs then
  # take from the build, never from the environment of a command an agent
  # runs (internal/hostpaths). Without them, they read the environment.
  configHome ? "",
  stateHome ? "",
}: let
  # -ldflags is split at spaces.
  hostLdflags = lib.concatLists (lib.mapAttrsToList (
      var: dir:
        lib.optionals (dir != "") (
          assert lib.assertMsg (!lib.hasInfix " " dir) "agent-tools: ${var} ${dir} holds a space, which -ldflags cannot pass";
            ["-X" "github.com/wesnel/agent-tools/internal/hostpaths.${var}=${dir}"]
        )
    ) {
      builtConfigHome = configHome;
      builtStateHome = stateHome;
    });

  # ECA's shell deny pattern for credentials, from the path prefixes the
  # guard reads from its config: a prefix under home matches wherever a
  # command names it, as ~/, $HOME/ or any other way, and a space in it
  # matches escaped too. A public key, ending .pub, is let through.
  credentialsPattern = home: paths: let
    relative = p: lib.removePrefix "${home}/" p;
    escape = p: lib.replaceStrings [" "] ["\\\\? "] (lib.escapeRegex (relative p));
  in "(?s).*(${lib.concatMapStringsSep "|" escape paths})(?![^\\s'\"]*\\.pub\\b).*";

  # credentialsPattern for a sample list, held to examples by postCheck.
  credentialsExample = {
    p = credentialsPattern "/home/u" ["/home/u/.ssh/id_" "/home/u/.codex/auth.json" "/home/u/Library/Application Support/mcp-proxy" "/var/secret/token"];
    deny = ["cat ~/.ssh/id_ed25519" "cat $HOME/.codex/auth.json" "cat ~/Library/Application\\ Support/mcp-proxy/c.json" "cat '/home/u/Library/Application Support/mcp-proxy/c.json'" "cat /var/secret/token"];
    allow = ["cat ~/.ssh/id_ed25519.pub" "ls ~/.ssh" "cat ~/.codex/config.toml"];
  };

  agent-tools = buildGoModule {
    pname = "agent-tools";
    version = "0.1.0";

    src = lib.fileset.toSource {
      root = ./.;
      fileset = lib.fileset.unions [./go.mod ./cmd ./internal];
    };

    vendorHash = null;
    env.CGO_ENABLED = "0";
    ldflags = ["-s" "-w"] ++ hostLdflags;

    # Without a list, the guard counts everything under home as a credential
    # where its config is absent.
    preBuild = lib.optionalString (credentialPaths != []) ''
      cat > internal/guard/credentials_built.go <<'EOF'
      package guard

      func init() {
        builtCredentialPaths = []string{${lib.concatMapStringsSep ", " builtins.toJSON credentialPaths}}
      }
      EOF
    '';

    # The rehearsal and signing tests build stacks with the real git, and sign
    # them with a throwaway SSH key.
    nativeCheckInputs = [git gnugrep jq openssh];

    # The guard's own test holds it to policy.json's examples. This holds
    # ECA's patterns to the same examples, and the credentials pattern to
    # its own, with PCRE standing in for Java: whole-input matches, as ECA
    # makes them, over input that may span lines.
    postCheck = ''
      {
        jq -c '.[] | {p: ("(?s)" + .pattern), deny, allow}' internal/guard/policy.json
        printf '%s\n' ${lib.escapeShellArg (builtins.toJSON credentialsExample)}
      } |
        while IFS= read -r rule; do
          pattern=$(jq -r .p <<<"$rule")
          for kind in deny allow; do
            for i in $(jq -r ".$kind | keys[]" <<<"$rule"); do
              command=$(jq -r ".$kind[$i]" <<<"$rule")
              if printf '%s' "$command" | grep -qzPx -- "$pattern"; then matched=deny; else matched=allow; fi
              if [ "$matched" != "$kind" ]; then
                echo "ECA pattern $pattern: wanted $kind for: $command" >&2
                exit 1
              fi
            done
          done
        done
    '';

    # ECA's shell deny patterns for each role, from the policy the guard is
    # tested against: a rule holds for its scope and every stricter role.
    # `(?s)' lets `.' cross a newline, without which a second line of a
    # command escapes ECA's whole-string match.
    passthru.ecaDenied = let
      rules = builtins.fromJSON (builtins.readFile ./internal/guard/policy.json);
      roles = ["baseline" "coordinator" "owner" "reviewer"];
      rank = role: lib.lists.findFirstIndex (r: r == role) (throw "policy.json: unknown scope ${role}") roles;
      upTo = role: map (r: "(?s)${r.pattern}") (builtins.filter (r: rank r.scope <= rank role) rules);
    in
      lib.genAttrs roles upTo;

    # credentialsPattern, for ECA's agents, which add it to every role's
    # patterns.
    passthru.credentialsPattern = credentialsPattern;

    # The MCP tools the guard lets through (reads) or asks the operator
    # about (confirms), per service, for mcp-proxy's tool filters.
    passthru.mcpTools = builtins.fromJSON (builtins.readFile ./internal/guard/mcp.json);

    # The reviewer's contract, which `git fresh-review` embeds for Codex, for
    # the agents' own reviewer sub-agents.
    passthru.reviewerPrompt = builtins.readFile ./internal/review/reviewer.md;

    # The security review's contract as `git fresh-review --focus security`
    # builds it (focus.contract in internal/review): the general contract up
    # to its Checks heading, then the security checks. TestSecurityContract
    # holds the Go side to the same heading.
    passthru.securityReviewerPrompt = let
      parts = lib.splitString "\n## Checks\n" (builtins.readFile ./internal/review/reviewer.md);
    in
      assert lib.assertMsg (builtins.length parts == 2) "agent-tools: reviewer.md needs exactly one `## Checks` heading, where the security contract splits it";
        builtins.head parts + "\n" + builtins.readFile ./internal/review/security.md;

    # The agents with a guard role, and the user-level Claude Code agents.
    passthru.guardAgents = builtins.fromJSON (builtins.readFile ./internal/guard/agents.json);

    # Static Linux builds of pr-loop and agent-plan for a sandbox guest, which
    # runs them as clients of `sandbox-relay serve` on the host.
    # The host's directories are not the guest's, so none is baked in.
    passthru.linuxClient = agent-tools.overrideAttrs (old: {
      pname = "pr-loop-linux-client";
      subPackages = ["cmd/pr-loop" "cmd/agent-plan"];
      ldflags = ["-s" "-w"];
      env =
        old.env
        // {
          GOOS = "linux";
          GOARCH = stdenv.hostPlatform.go.GOARCH;
        };
      doCheck = false;
      postInstall = ''
        if [ -d $out/bin/linux_${stdenv.hostPlatform.go.GOARCH} ]; then
          mv $out/bin/linux_${stdenv.hostPlatform.go.GOARCH}/* $out/bin/
          rmdir $out/bin/linux_${stdenv.hostPlatform.go.GOARCH}
        fi
      '';
    });

    meta.description = "pr-loop (the review loop), git-sign-pass (the signing pass), git-red-check (checks a commit's tests fail without its change), git-fresh-review (reviews a range of commits with fresh eyes), git-push-guard (keeps private history out of public repositories), sandbox-relay (host programs for a sandbox), agent-plan (agents' plan files and event logs) and plan-dash (the Agent plans page's documents)";
  };
in
  agent-tools
