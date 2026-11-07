refuse() {
  echo "trash-scratch: refused: $1" >&2
  exit 2
}

[[ $# -gt 0 ]] || refuse 'name at least one scratch path'
# Git queries must not run a configured filesystem monitor or use a caller's
# alternate index, repository or configuration environment.
for name in "${!GIT_@}"; do unset "$name"; done
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_COUNT=0
git_read() { git -c core.fsmonitor=false -c core.untrackedCache=false "$@"; }

# On a case-insensitive file system two spellings name one file, and
# realpath keeps the one it was given, so paths are compared folded.
fold() {
  if "$case_insensitive"; then printf '%s' "${1,,}"; else printf '%s' "$1"; fi
}
pathspec_magic=literal
if "$case_insensitive"; then pathspec_magic=literal,icase; fi
# within PATH DIR: PATH is DIR or under it.
within() { [[ "$(fold "$1")" == "$(fold "$2")" || "$(fold "$1")" == "$(fold "$2")/"* ]]; }

repo=$(git_read rev-parse --show-toplevel 2>/dev/null) || repo=''
if [[ -n "$repo" ]]; then repo=$(realpath -e -- "$repo"); fi

# linked_worktree DIR: whether DIR is in a linked worktree, not a main
# checkout, where the operator's own work is.
linked_worktree() {
  local dirs
  dirs=$(git_read -C "$1" rev-parse --path-format=absolute --git-dir --git-common-dir 2>/dev/null) || return 1
  [[ "${dirs%%$'\n'*}" != "${dirs#*$'\n'}" ]]
}

# This session's own scratch directory, named by its ID under the scratch
# base; another session's beside it is not this one's to remove. And the
# linked worktrees agent-plan worktrees gives this session, such as its task
# worktrees while the working directory is the main checkout: those this
# host's plans and parts naming the session name, but those OWNERS gives
# another agent. It reads the plan files without pulling the notes
# repository, so a delete waits on no network. Where it fails, no plan's
# worktree holds scratch.
session=''
plan_trees=()
plans_failure=''
if [[ "${CLAUDE_CODE_SESSION_ID:-}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  for candidate in "$scratch_base"/*/"$CLAUDE_CODE_SESSION_ID"; do
    if [[ -d "$candidate" ]]; then session=$(realpath -e -- "$candidate"); fi
  done
  if listed=$("$agent_plan" worktrees --session "claude:$CLAUDE_CODE_SESSION_ID" 2>/dev/null); then
    while IFS= read -r tree; do
      tree=$(realpath -e -- "$tree" 2>/dev/null) || continue
      if linked_worktree "$tree"; then plan_trees+=("$tree"); fi
    done <<<"$listed"
  else
    plans_failure="; agent-plan could not list this session's plan worktrees (exit $?), which \`agent-plan worktrees --session claude:$CLAUDE_CODE_SESSION_ID\` explains"
  fi
fi

paths=()
for arg in "$@"; do
  path=$(realpath -e -- "$arg") || refuse "cannot resolve $arg"
  # What the sandbox keeps from a sandboxed command's writes, and every
  # directory holding one.
  for protected in "${protected_paths[@]}"; do
    protected=$(realpath -m -- "$protected")
    if within "$path" "$protected" || within "$protected" "$path"; then
      refuse "$arg is a path the sandbox protects"
    fi
  done
  allowed=false
  # The worktree holding the path: the working directory's, else one of
  # this session's plans names.
  tree=''
  for candidate in ${repo:+"$repo"} "${plan_trees[@]}"; do
    if within "$path" "$candidate"; then
      tree=$candidate
      break
    fi
  done
  if [[ -n "$tree" ]]; then
    [[ "$(fold "$path")" != "$(fold "$tree")" ]] || refuse 'the worktree root is not scratch'
    relative=${path:${#tree}+1}
    ! within "$relative" .git || refuse 'Git metadata is not scratch'
    ! within "$relative" .claude || refuse "$arg is Claude Code's project configuration, which the sandbox protects"
    tracked=$(git_read -C "$tree" ls-files -- ":($pathspec_magic)$relative") || refuse 'cannot inspect tracked paths'
    [[ -z "$tracked" ]] || refuse "$arg contains a tracked path"
    allowed=true
  elif [[ -n "$session" ]] && within "$path" "$session"; then
    [[ "$(fold "$path")" != "$(fold "$session")" ]] || refuse "the session's scratch directory itself is not scratch"
    ! within "$path" "$session/tasks" || refuse "the session's task output is not scratch"
    allowed=true
  fi
  "$allowed" || refuse "$arg is outside the working directory worktree, the linked worktrees this session's plans name and give no other agent, and this session's scratch directory$plans_failure"
  paths+=("$path")
done

# Validate the entire argument list before the first move to Trash. macOS's
# trash reads -- as a file name, and every path is absolute, so none starts
# with -.
exec "$trash_command" "${paths[@]}"
