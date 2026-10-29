# The operator's way into a review loop: an ordinary session of the harness
# they choose, held by its sandbox and the baseline guard like any other,
# with the loop's mode in its environment, which pr-loop gates its writes
# on. `claude-review-loop` is `review-loop --harness claude`, by the name it
# is run as. The guard refuses both, and any setting of PR_LOOP_SESSION or
# PR_LOOP_MODE, to agents.
{
  lib,
  pkgs,
  # The commands a Codex loop waits for the operator to approve, named to
  # them when it starts.
  approved,
}: let
  # "$defaults" is literal: Claude Code splices its own auto-mode rules in
  # there.
  claudeSettings = builtins.toJSON {
    env.PR_LOOP_SESSION = "1";
    permissions.allow = ["Bash(pr-loop *)"];
    autoMode.environment = [
      "$defaults"
      "This is the operator's review-loop session over the pull requests it was started for. Its GitHub writes go through `pr-loop`, which holds them to the loop's mode, PR_LOOP_MODE."
    ];
  };

  notice = "review-loop: in Codex, each run of ${lib.concatStringsSep ", " approved} waits for your approval.";

  launcher = pkgs.writeShellApplication {
    name = "review-loop";
    runtimeInputs = [pkgs.jq];
    text = ''
      usage() {
        echo "usage: review-loop --harness claude|codex --bots|--people PR..." >&2
        echo "       claude-review-loop --bots|--people PR..." >&2
        exit 2
      }
      if [[ "''${0##*/}" == claude-review-loop ]]; then
        harness=claude
      else
        [[ "''${1-}" == --harness && $# -ge 2 ]] || usage
        harness=$2
        shift 2
      fi
      case "$harness" in
        claude | codex) ;;
        *) usage ;;
      esac
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
          settings=$(jq -c --arg mode "$mode" '.env.PR_LOOP_MODE = $mode' <<<${lib.escapeShellArg claudeSettings})
          exec claude --settings "$settings" --permission-mode auto "/review-loop $*"
          ;;
        codex)
          ${lib.optionalString (approved != []) "echo ${lib.escapeShellArg notice} >&2"}
          # --no-daemon: a shared background server would run the loop's
          # commands in its own environment, without the loop's variables.
          exec codex --no-daemon \
            -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' \
            -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"" \
            "\$review-loop Run the operator's $mode-mode review loop on these pull requests: $*"
          ;;
      esac
    '';
  };

  mkReviewLoop = {postBuild ? ""}:
    pkgs.symlinkJoin {
      name = "review-loop";
      paths = [launcher];
      postBuild = "ln -s review-loop $out/bin/claude-review-loop\n" + postBuild;
    };

  # The launcher under test, with a claude and a codex that print what they
  # were given: a test never starts a signed-in session.
  tests = let
    fake = name:
      pkgs.writeShellScriptBin name ''
        printf '%s\n' "$PR_LOOP_SESSION" "$PR_LOOP_MODE" "$@"
        exit "''${REVIEW_LOOP_TEST_EXIT:-0}"
      '';
    bin = "${mkReviewLoop {}}/bin";
  in
    pkgs.runCommand "review-loop-tests" {nativeBuildInputs = [pkgs.jq];} ''
      export PATH=${fake "claude"}/bin:${fake "codex"}/bin:$PATH
      for launch in "${bin}/review-loop --harness claude" "${bin}/claude-review-loop"; do
        out_=$($launch --people 12 '#13')
        [[ "$(sed -n 1,3p <<<"$out_")" == $'1\npeople\n--settings' ]]
        jq -e '.env == {PR_LOOP_SESSION: "1", PR_LOOP_MODE: "people"}
          and .permissions.allow == ["Bash(pr-loop *)"]
          and .autoMode.environment[0] == "$defaults"' <<<"$(sed -n 4p <<<"$out_")"
        [[ "$(sed -n 5,7p <<<"$out_")" == $'--permission-mode\nauto\n/review-loop 12 #13' ]]
        $launch --bots 7 | sed -n 4p | jq -e '.env.PR_LOOP_MODE == "bots"'
      done
      for mode in bots people; do
        out_=$(${bin}/review-loop --harness codex --$mode 12 '#13' 2>err)
        expected=$(printf '%s\n' 1 "$mode" --no-daemon \
          -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' \
          -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"" \
          "\$review-loop Run the operator's $mode-mode review loop on these pull requests: 12 #13")
        [[ "$out_" == "$expected" ]]
        [[ "$(cat err)" == ${lib.escapeShellArg (lib.optionalString (approved != []) notice)} ]]
      done
      for bad in "" "--bots 1" "--harness" "--harness other --bots 1" "--harness codex --both 1" \
        "--harness codex --bots" "--harness claude --bots x" "--harness codex --bots 0" "--harness codex --bots 1 --search"; do
        status=0
        ${bin}/review-loop $bad >bad-output 2>bad-error || status=$?
        [[ "$status" == 2 && ! -s bad-output ]]
        grep -F 'usage: review-loop' bad-error
      done
      status=0
      ${bin}/claude-review-loop --harness codex --bots 1 >bad-output 2>bad-error || status=$?
      [[ "$status" == 2 && ! -s bad-output ]]
      for harness in claude codex; do
        status=0
        REVIEW_LOOP_TEST_EXIT=17 ${bin}/review-loop --harness "$harness" --bots 7 >/dev/null 2>&1 || status=$?
        [[ "$status" == 17 ]]
      done
      touch $out
    '';
in
  mkReviewLoop {postBuild = ": ${tests}";}
