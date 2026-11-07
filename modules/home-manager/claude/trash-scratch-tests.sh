set -euo pipefail
command_under_test=("$@")
root=$(mktemp -d)
export HOME="$root/home" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export TRASH_TEST_DEST="$root/trashed"
export TRASH_TEST_ROOT="$root"
# The session's scratch is its own directory under the scratch base, named
# by its ID; another session's sits beside it.
export CLAUDE_CODE_SESSION_ID=this-session
session="$root/scratch/project/this-session"
mkdir -p "$HOME" "$TRASH_TEST_DEST" "$root/repo" "$root/outside" "$session/tasks" "$root/scratch/project/other-session"
cd "$root/repo"
git init -q
mkdir tracked-dir
printf keep >tracked-dir/keep
git add tracked-dir/keep
git -c user.name=test -c user.email=test@example.com commit -qm fixture
# Task worktrees of the same repository outside the working directory, as
# Claude Code's are: one this session's plan names, one only another
# session's plan names, one only another host's plan names, two this
# session's plan gives owners: another agent, and main, the session's own;
# and those this session's plan names whose owner or session other plans
# decide.
for tree in task other-task host-task agent-task main-task cross-task masked-task unmasked-task later-task nolog-task taken-task; do
  git worktree add -q --detach "$root/$tree"
done
# And a worktree with nothing tracked, whose root only the root's own check
# refuses.
git init -q "$root/fresh-repo"
git -C "$root/fresh-repo" -c user.name=test -c user.email=test@example.com commit -q --allow-empty -m empty
git -C "$root/fresh-repo" worktree add -q --detach "$root/fresh"
mkdir -p "$root/plans"
# plan FILE WORKTREES SESSIONS [OWNERS]: a plan's file and, for a plan's
# own file, its log, without which agent-plan does not open the plan.
plan() {
  printf '#+title: A plan\n\n* Plan\n:PROPERTIES:\n:CUSTOM_ID: %s\n:WORKTREES: %s\n:SESSIONS: %s\n:OWNERS: %s\n:END:\n** Goal\n' \
    "${1%.org}" "$2" "$3" "${4:-}" >"$root/plans/$1"
  if [[ "$1" != *.P[0-9]*.org ]]; then printf '' >"$root/plans/${1%.org}.log.org"; fi
}
# This session's plan names the main checkout too, as plans of unplanned
# work do; its part names the worktree OWNERS gives another agent, as a
# sub-agent's part does.
plan 20261106000000-this.testhost.org \
  "$root/missing $root/repo $root/task $root/fresh $root/agent-task $root/main-task $root/cross-task $root/masked-task $root/unmasked-task $root/later-task $root/taken-task" \
  'codex:other claude:this-session' \
  "$root/agent-task=agent-1 $root/main-task=main $root/masked-task=main $root/unmasked-task=agent-1 $root/later-task=main"
plan 20261106000000-this.testhost.P1.org "$root/agent-task" claude:this-session
plan 20261106000001-other.testhost.org "$root/other-task" claude:other-session
plan 20261106000002-this.otherhost.org "$root/host-task" claude:this-session
# Owners other plans give: the newest plan's owner of a worktree wins, an
# old-form YYYY-MM-DD name read as YYYYMMDD; a plan without its log names
# nothing. A newer plan naming a worktree, in OWNERS too, names this
# session, or the worktree is its other session's.
plan 2026-11-05-cross.testhost.org '' claude:other-session "$root/cross-task=agent-1"
plan 20261106000003-newer.testhost.org '' claude:this-session "$root/masked-task=agent-1 $root/unmasked-task=main"
plan 2026-11-07-later.testhost.org '' claude:this-session "$root/later-task=agent-1"
plan 20261106000004-nolog.testhost.org "$root/nolog-task" claude:this-session
plan 20261106000005-taken.testhost.org "$root/taken-task" claude:other-session
mv "$root/plans/20261106000004-nolog.testhost.log.org" "$root/nolog.log.org"
# agent-plan reads these plans as this host's; with a config it cannot read
# it fails.
printf '{"plans":{"dir":"%s","host":"testhost"}}' "$root/plans" >"$root/agent-tools.json"
export AGENT_TOOLS_CONFIG="$root/agent-tools.json"
printf '{' >"$root/broken.json"
refuses() {
  if "${command_under_test[@]}" "$@"; then
    echo "accepted forbidden candidate: $*" >&2
    exit 1
  fi
  test -z "$(ls -A "$TRASH_TEST_DEST")"
}
# refuses_with MESSAGE ARG...: refused, with MESSAGE in the refusal, which
# shows which check refused it.
refuses_with() {
  local message=$1 err
  shift
  if err=$("${command_under_test[@]}" "$@" 2>&1); then
    echo "accepted forbidden candidate: $*" >&2
    exit 1
  fi
  if [[ "$err" != *"$message"* ]]; then
    echo "refused $* for another reason: $err" >&2
    exit 1
  fi
  test -z "$(ls -A "$TRASH_TEST_DEST")"
}
refuses tracked-dir/keep
if GIT_INDEX_FILE="$root/empty-index" "${command_under_test[@]}" tracked-dir/keep; then
  echo 'alternate index hid a tracked path' >&2
  exit 1
fi
if GIT_DIR="$root/repo/.git" GIT_WORK_TREE="$root" "${command_under_test[@]}" tracked-dir/keep; then
  echo 'alternate worktree hid a tracked path' >&2
  exit 1
fi
refuses tracked-dir
refuses .
refuses .git
# On a case-insensitive file system, as macOS's is by default, these name
# the tracked directory and the Git metadata; elsewhere they do not exist.
refuses TRACKED-DIR
refuses TRACKED-DIR/KEEP
refuses .GIT
ln -s .git metadata-alias
refuses metadata-alias
printf outside >"$root/outside/file"
ln -s "$root/outside/file" escape
refuses escape
refuses ../outside/file
printf scratch >untracked
refuses untracked tracked-dir/keep
# A worktree nested in the working directory's tracks files its index does
# not list: neither they, its untracked files, its Git metadata, nor a
# directory holding one is scratch.
mkdir -p holder
for inner in inner holder/inner; do
  git init -q "$inner"
  printf kept >"$inner/kept"
  git -C "$inner" add kept
  git -C "$inner" -c user.name=test -c user.email=test@example.com commit -qm nested
done
printf loose >inner/loose
refuses_with 'is in a worktree nested in' inner/kept
refuses_with 'is in a worktree nested in' inner/loose
refuses_with 'Git metadata is not scratch' inner/.git/config
refuses_with 'Git metadata is not scratch' inner/.git
refuses_with 'holds a nested worktree' inner
refuses_with 'holds a nested worktree' holder
# A directory the check cannot read may hide one.
mkdir -p locked-holder/locked
git init -q locked-holder/locked/inner
chmod 000 locked-holder/locked
trap 'chmod -R u+rwX "$root" 2>/dev/null || true' EXIT
if [[ -r locked-holder/locked ]]; then
  echo 'chmod 000 left a directory readable, as for root, so the unreadable-directory case cannot run' >&2
  exit 1
fi
refuses_with 'cannot inspect locked-holder for nested worktrees' locked-holder
chmod 755 locked-holder/locked
# What the sandbox keeps from a sandboxed command's writes: Claude Code's
# settings in the project, and the paths this module denies (here one in
# the worktree, as the plans directory is in the notes repository).
mkdir -p .claude protected
printf '{}' >.claude/settings.local.json
printf lock >protected/log.lock
refuses .claude/settings.local.json
refuses .claude
refuses protected/log.lock
refuses protected
# Another session's scratch, the scratch base, this session's directory
# itself and its task output.
printf other >"$root/scratch/project/other-session/file"
refuses "$root/scratch/project/other-session/file"
refuses "$root/scratch/project/other-session"
refuses "$root/scratch/project"
refuses "$session"
printf task >"$session/tasks/output"
refuses "$session/tasks/output"
refuses "$session/tasks"
printf temporary >"$session/temporary"
if CLAUDE_CODE_SESSION_ID='' "${command_under_test[@]}" "$session/temporary"; then
  echo 'accepted scratch with no session' >&2
  exit 1
fi
# A worktree this session's plan names, with the working directory in
# another, has the working directory worktree's checks; a worktree only
# another session's plan, or another host's, names is not this session's.
printf review >"$root/task/review"
printf other >"$root/other-task/review"
printf host >"$root/host-task/review"
printf fresh >"$root/fresh/file"
refuses "$root/task/tracked-dir/keep"
refuses "$root/task/tracked-dir"
refuses "$root/task"
refuses "$root/fresh"
refuses "$root/task/.git"
refuses_with 'is outside the working directory worktree' "$root/other-task/review"
refuses_with 'is outside the working directory worktree' "$root/host-task/review"
# Neither a main checkout, where the operator's own work is, nor a worktree
# OWNERS gives another agent is this session's through its plan.
printf main >main-work
printf agent >"$root/agent-task/review"
printf main-task >"$root/main-task/owned"
(cd "$root/task" && refuses_with 'is outside the working directory worktree' "$root/repo/main-work")
refuses_with 'is outside the working directory worktree' "$root/agent-task/review"
# Whichever plan gives a worktree its owner: the newest plan's owner wins,
# and a plan without its log names nothing. A newer plan of another session
# naming a worktree makes it that session's.
for tree in cross-task masked-task unmasked-task later-task nolog-task taken-task; do printf '%s' "$tree" >"$root/$tree/$tree"; done
refuses_with 'is outside the working directory worktree' "$root/cross-task/cross-task"
refuses_with 'is outside the working directory worktree' "$root/masked-task/masked-task"
refuses_with 'is outside the working directory worktree' "$root/later-task/later-task"
refuses_with 'is outside the working directory worktree' "$root/nolog-task/nolog-task"
refuses_with 'is outside the working directory worktree' "$root/taken-task/taken-task"
# With no plan naming the session, only the working directory worktree and
# the session's scratch directory hold scratch.
CLAUDE_CODE_SESSION_ID=lone-session refuses_with 'is outside the working directory worktree' "$root/task/review"
# Where agent-plan fails, no plan's worktree holds scratch, and the working
# directory worktree and the session's scratch directory still do.
AGENT_TOOLS_CONFIG="$root/broken.json" refuses_with 'agent-plan could not list' "$root/task/review"
printf broken >broken-config
printf broken >"$session/broken-scratch"
printf lone >lone
test -f untracked
test -f tracked-dir/keep
test -f "$root/outside/file"
test -f "$root/task/tracked-dir/keep"
"${command_under_test[@]}" untracked
test ! -e untracked
test "$(cat "$TRASH_TEST_DEST/untracked")" = scratch
"${command_under_test[@]}" "$session/temporary"
test ! -e "$session/temporary"
test "$(cat "$TRASH_TEST_DEST/temporary")" = temporary
"${command_under_test[@]}" "$root/task/review"
test ! -e "$root/task/review"
test "$(cat "$TRASH_TEST_DEST/review")" = review
"${command_under_test[@]}" "$root/fresh/file"
test "$(cat "$TRASH_TEST_DEST/file")" = fresh
(cd "$root/task" && "${command_under_test[@]}" "$root/main-task/owned")
test "$(cat "$TRASH_TEST_DEST/owned")" = main-task
"${command_under_test[@]}" "$root/unmasked-task/unmasked-task"
test "$(cat "$TRASH_TEST_DEST/unmasked-task")" = unmasked-task
CLAUDE_CODE_SESSION_ID=lone-session "${command_under_test[@]}" lone
test ! -e lone
test "$(cat "$TRASH_TEST_DEST/lone")" = lone
AGENT_TOOLS_CONFIG="$root/broken.json" "${command_under_test[@]}" broken-config
test ! -e broken-config
AGENT_TOOLS_CONFIG="$root/broken.json" "${command_under_test[@]}" "$session/broken-scratch"
test ! -e "$session/broken-scratch"
echo 'trash-scratch: refusal and untracked-file checks passed'
