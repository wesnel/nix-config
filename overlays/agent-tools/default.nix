{
  buildGoModule,
  git,
  gnugrep,
  jq,
  lib,
  openssh,
  stdenv,
}: let
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

    # The rehearsal and signing tests build stacks with the real git, and sign
    # them with a throwaway SSH key.
    nativeCheckInputs = [git gnugrep jq openssh];

    # The guard's own test holds it to policy.json's examples. This holds
    # ECA's patterns to the same examples, with PCRE standing in for Java:
    # whole-input matches, as ECA makes them, over input that may span lines.
    postCheck = ''
      jq -c '.[] | {p: ("(?s)" + .pattern), deny, allow}' internal/guard/policy.json |
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

    # ECA's shell deny patterns for the review loop's agents, from the policy
    # the guard is tested against. `(?s)' lets `.' cross a newline, without
    # which a second line of a command escapes ECA's whole-string match.
    passthru.ecaDenied = let
      rules = builtins.fromJSON (builtins.readFile ./internal/guard/policy.json);
      patterns = map (r: "(?s)${r.pattern}");
    in {
      coordinator = patterns (builtins.filter (r: !(r.ownerOnly or false) && !(r.reviewerOnly or false)) rules);
      owner = patterns (builtins.filter (r: !(r.reviewerOnly or false)) rules);
      reviewer = patterns rules;
    };

    # The reviewer's contract, which `git fresh-review` embeds for Codex, for
    # the agents' own reviewer sub-agents.
    passthru.reviewerPrompt = builtins.readFile ./internal/review/reviewer.md;

    # A static Linux build of pr-loop for a sandbox guest, which runs it as a
    # client of `sandbox-relay serve` on the host.
    passthru.linuxClient = agent-tools.overrideAttrs (old: {
      pname = "pr-loop-linux-client";
      subPackages = ["cmd/pr-loop"];
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

    meta.description = "pr-loop (the review loop), git-sign-pass (the signing pass), git-red-check (checks a commit's tests fail without its change), git-push-guard (keeps private history out of public repositories) and sandbox-relay (host programs for a sandbox)";
  };
in
  agent-tools
