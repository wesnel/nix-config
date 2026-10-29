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

# This session's own scratch directory, named by its ID under the scratch
# base; another session's beside it is not this one's to remove.
session=''
if [[ "${CLAUDE_CODE_SESSION_ID:-}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  for candidate in "$scratch_base"/*/"$CLAUDE_CODE_SESSION_ID"; do
    if [[ -d "$candidate" ]]; then session=$(realpath -e -- "$candidate"); fi
  done
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
  if [[ -n "$repo" ]] && within "$path" "$repo"; then
    [[ "$(fold "$path")" != "$(fold "$repo")" ]] || refuse 'the worktree root is not scratch'
    relative=${path:${#repo}+1}
    ! within "$relative" .git || refuse 'Git metadata is not scratch'
    ! within "$relative" .claude || refuse "$arg is Claude Code's project configuration, which the sandbox protects"
    tracked=$(git_read -C "$repo" ls-files -- ":($pathspec_magic)$relative") || refuse 'cannot inspect tracked paths'
    [[ -z "$tracked" ]] || refuse "$arg contains a tracked path"
    allowed=true
  elif [[ -n "$session" ]] && within "$path" "$session"; then
    [[ "$(fold "$path")" != "$(fold "$session")" ]] || refuse "the session's scratch directory itself is not scratch"
    ! within "$path" "$session/tasks" || refuse "the session's task output is not scratch"
    allowed=true
  fi
  "$allowed" || refuse "$arg is outside the working directory worktree and this session's scratch directory"
  paths+=("$path")
done

# Validate the entire argument list before the first move to Trash. macOS's
# trash reads -- as a file name, and every path is absolute, so none starts
# with -.
exec "$trash_command" "${paths[@]}"
