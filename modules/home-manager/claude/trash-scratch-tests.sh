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
refuses() {
  if "${command_under_test[@]}" "$@"; then
    echo "accepted forbidden candidate: $*" >&2
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
test -f untracked
test -f tracked-dir/keep
test -f "$root/outside/file"
"${command_under_test[@]}" untracked
test ! -e untracked
test "$(cat "$TRASH_TEST_DEST/untracked")" = scratch
"${command_under_test[@]}" "$session/temporary"
test ! -e "$session/temporary"
test "$(cat "$TRASH_TEST_DEST/temporary")" = temporary
echo 'trash-scratch: refusal and untracked-file checks passed'
