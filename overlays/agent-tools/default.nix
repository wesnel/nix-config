{
  buildGoModule,
  git,
  gnugrep,
  jq,
  lib,
  openssh,
  procps,
  stdenv,
  # wgn.home.llm.guard.credentialPaths, a path under home as ~/…, which the
  # guard falls back on where its config is absent.
  credentialPaths ? [],
}: let
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
    ldflags = ["-s" "-w"];

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
    # them with a throwaway SSH key. pr-loop stop finds a session's
    # processes with ps.
    nativeCheckInputs = [git gnugrep jq openssh procps];

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

    # Static Linux builds for a sandbox guest: pr-loop and agent-plan, which
    # it runs as clients of `sandbox-relay serve` on the host, and
    # git-red-check, which `pr-loop review` runs in the VM.
    passthru.linuxClient = agent-tools.overrideAttrs (old: {
      pname = "pr-loop-linux-client";
      subPackages = ["cmd/pr-loop" "cmd/agent-plan" "cmd/git-red-check"];
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

    meta.description = "pr-loop (the review loop), git-sign-pass (the signing pass), git-red-check (checks a commit's tests fail without its change), git-fresh-review (reviews a range of commits with fresh eyes), git-push-guard (keeps private history out of public repositories), sandbox-relay (host programs for a sandbox) and agent-plan (agents' plan files and event logs)";
  };
in
  agent-tools
