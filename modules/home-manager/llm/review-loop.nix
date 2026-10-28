{
  lib,
  pkgs,
}: let
  mkReviewLoop = {postCheck ? ""}:
    pkgs.writeShellApplication {
      name = "review-loop";
      runtimeInputs = [pkgs.jq];
      derivationArgs = {inherit postCheck;};
      text = ''
        usage() {
          echo "usage: review-loop --harness claude|codex --bots|--people PR..." >&2
          exit 2
        }
        [[ "''${1-}" == --harness ]] || usage
        harness=''${2-}
        case "$harness" in claude|codex) ;; *) usage ;; esac
        shift 2
        case "''${1-}" in
          --bots) mode=bots ;;
          --people) mode=people ;;
          *) usage ;;
        esac
        shift
        [[ $# -gt 0 ]] || usage
        for pr in "$@"; do
          [[ "$pr" =~ ^#?[1-9][0-9]*$ ]] || usage
        done
        export PR_LOOP_SESSION=1 PR_LOOP_MODE="$mode"
        case "$harness" in
          claude)
            # shellcheck disable=SC2016
            settings=$(jq -c --arg mode "$mode" '.env.PR_LOOP_MODE = $mode' <<<${lib.escapeShellArg (builtins.toJSON {
          env.PR_LOOP_SESSION = "1";
          permissions.allow = ["Bash(pr-loop *)"];
          autoMode.environment = [
            "$defaults"
            "This is the operator's review-loop session over the pull requests it was started for. Its GitHub writes go through `pr-loop`, which holds them to the loop's mode, PR_LOOP_MODE."
          ];
        })})
            exec claude --settings "$settings" --permission-mode auto "/review-loop $*"
            ;;
          codex)
            exec codex --no-daemon \
              -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' \
              -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"" \
              "Use the review-loop skill to run the operator's $mode review loop on PRs: $*. Read the skill before proceeding."
            ;;
        esac
      '';
    };
  tests = let
    # Model CLIs are stand-ins: tests must never start authenticated sessions.
    fake = name:
      pkgs.writeShellScriptBin name ''
        printf '%s\n' "$PR_LOOP_SESSION" "$PR_LOOP_MODE" "$@"
        exit "''${REVIEW_LOOP_TEST_EXIT:-0}"
      '';
    launcher = lib.getExe (mkReviewLoop {});
  in
    pkgs.runCommand "review-loop-tests" {nativeBuildInputs = [pkgs.jq];} ''
      export PATH=${fake "claude"}/bin:${fake "codex"}/bin:$PATH
      out_=$(${launcher} --harness claude --people 12 '#13')
      [[ "$(sed -n 1,3p <<<"$out_")" == $'1\npeople\n--settings' ]]
      jq -e '.env == {PR_LOOP_SESSION: "1", PR_LOOP_MODE: "people"}
        and .permissions.allow == ["Bash(pr-loop *)"]
        and .autoMode.environment[0] == "$defaults"' <<<"$(sed -n 4p <<<"$out_")"
      [[ "$(sed -n 5,7p <<<"$out_")" == $'--permission-mode\nauto\n/review-loop 12 #13' ]]
      ${launcher} --harness claude --bots 7 | sed -n 4p | jq -e '.env.PR_LOOP_MODE == "bots"'
      for mode in bots people; do
        out_=$(${launcher} --harness codex --$mode 12 '#13')
        [[ "$(sed -n 1,3p <<<"$out_")" == "$(printf '1\n%s\n--no-daemon' "$mode")" ]]
        [[ "$(sed -n 4,7p <<<"$out_")" == "$(printf '%s\n' -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"")" ]]
        [[ "$(sed -n 8p <<<"$out_")" == "Use the review-loop skill to run the operator's $mode review loop on PRs: 12 #13. Read the skill before proceeding." ]]
        [[ "$(wc -l <<<"$out_" | tr -d ' ')" == 8 ]]
      done
      for bad in "" "--bots 1" "--harness" "--harness other --bots 1" "--harness codex --both 1" "--harness codex --bots" "--harness claude --bots x" "--harness codex --bots 0" "--harness codex --bots 1 --search"; do
        status=0
        # shellcheck disable=SC2086
        ${launcher} $bad > bad-output 2> bad-error || status=$?
        [[ "$status" == 2 && ! -s bad-output ]]
        grep -F 'usage: review-loop' bad-error
      done
      for harness in claude codex; do
        status=0
        REVIEW_LOOP_TEST_EXIT=17 ${launcher} --harness "$harness" --bots 7 >/dev/null || status=$?
        [[ "$status" == 17 ]]
      done
      touch $out
    '';
in
  mkReviewLoop {postCheck = ": ${tests}";}
