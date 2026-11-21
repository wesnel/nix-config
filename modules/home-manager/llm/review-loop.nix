# The operator's way into a review loop: an ordinary session of the harness
# they choose, held by its sandbox and the baseline guard like any other,
# with the loop's mode in its environment, which agent-pr gates its writes
# on. `claude-review-loop` is `review-loop --harness claude`, by the name it
# is run as. The guard refuses both, and any setting of PR_LOOP_SESSION or
# PR_LOOP_MODE, to agents.
{
  lib,
  pkgs,
}: let
  loopTools = [
    "agent_pr_ci"
    "agent_pr_draft"
    "agent_pr_end"
    "agent_pr_link"
    "agent_pr_list"
    "agent_pr_logs"
    "agent_pr_merge_preview"
    "agent_pr_publication_status"
    "agent_pr_open"
    "agent_pr_push"
    "agent_pr_reply"
    "agent_pr_request"
    "agent_pr_sign_check"
    "agent_pr_stack"
    "agent_pr_status"
    "agent_pr_threads"
    "agent_pr_wait"
    "agent_review"
    "agent_review_ask"
    "agent_review_settle"
    "agent_review_show"
    "github_file"
    "github_pr_diff"
    "github_pr_view"
    "github_profile"
  ];
  approvalArgs = lib.concatMapStringsSep " " (tool: "-c ${lib.escapeShellArg "mcp_servers.agent_ops.tools.${tool}.approval_mode=\"approve\""}") loopTools;
  # "$defaults" is literal: Claude Code splices its own auto-mode rules in
  # there.
  claudeSettings = builtins.toJSON {
    env.PR_LOOP_SESSION = "1";
    permissions.allow = ["Bash(agent-pr *)"];
    autoMode.environment = [
      "$defaults"
      "This is the operator's review-loop session over the pull requests it was started for. Its GitHub writes go through `agent-pr`, which holds them to the loop's mode, PR_LOOP_MODE."
    ];
  };

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
          # --no-daemon: a shared background server would run the loop's
          # commands in its own environment, without the loop's variables.
          exec codex --no-daemon \
            -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' \
            -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"" \
            ${approvalArgs} \
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
          and .permissions.allow == ["Bash(agent-pr *)"]
          and .autoMode.environment[0] == "$defaults"' <<<"$(sed -n 4p <<<"$out_")"
        [[ "$(sed -n 5,7p <<<"$out_")" == $'--permission-mode\nauto\n/review-loop 12 #13' ]]
        $launch --bots 7 | sed -n 4p | jq -e '.env.PR_LOOP_MODE == "bots"'
      done
      for mode in bots people; do
        out_=$(${bin}/review-loop --harness codex --$mode 12 '#13' 2>err)
        expected=$(printf '%s\n' 1 "$mode" --no-daemon \
          -c 'shell_environment_policy.set.PR_LOOP_SESSION="1"' \
          -c "shell_environment_policy.set.PR_LOOP_MODE=\"$mode\"")
        for tool in agent_pr_ci agent_pr_draft agent_pr_end agent_pr_link agent_pr_list agent_pr_logs agent_pr_merge_preview agent_pr_publication_status \
          agent_pr_open agent_pr_push agent_pr_reply agent_pr_request agent_pr_sign_check agent_pr_stack \
          agent_pr_status agent_pr_threads agent_pr_wait agent_review agent_review_ask agent_review_settle \
          agent_review_show github_file github_pr_diff github_pr_view github_profile; do
          expected+=$(printf '\n%s\n%s' -c "mcp_servers.agent_ops.tools.$tool.approval_mode=\"approve\"")
        done
        expected+=$(printf '\n%s' "\$review-loop Run the operator's $mode-mode review loop on these pull requests: 12 #13")
        [[ "$out_" == "$expected" ]] || { echo "Codex launcher arguments differ" >&2; exit 1; }
        [[ ! -s err ]] || { cat err >&2; exit 1; }
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
