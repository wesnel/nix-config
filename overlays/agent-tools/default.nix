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

    # The reviewer's contract, which `git fresh-review` embeds for Codex, for
    # the agents' own reviewer sub-agents.
    passthru.reviewerPrompt = builtins.readFile ./internal/review/reviewer.md;

    # Static Linux builds of pr-loop and agent-plan for a sandbox guest, which
    # runs them as clients of `sandbox-relay serve` on the host.
    passthru.linuxClient = agent-tools.overrideAttrs (old: {
      pname = "pr-loop-linux-client";
      subPackages = ["cmd/pr-loop" "cmd/agent-plan"];
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
