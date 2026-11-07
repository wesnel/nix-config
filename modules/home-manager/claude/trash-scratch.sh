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

# read_plan FILE: sets plan_sessions, plan_worktrees and plan_owners to the
# SESSIONS, WORKTREES and OWNERS of plan file FILE, which agent-plan writes
# in the property drawer under its "* Plan" heading. The files are read
# here, not through agent-plan find, which pulls the notes repository first.
read_plan() {
  local line at=start
  plan_sessions='' plan_worktrees='' plan_owners=''
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$at" == start ]]; then
      if [[ "$line" =~ ^\*[[:space:]]+Plan[[:space:]]*$ ]]; then at=heading; fi
    elif [[ "$at" == heading ]]; then
      if [[ "$line" =~ ^[[:space:]]*:PROPERTIES:[[:space:]]*$ ]]; then at=drawer; else break; fi
    elif [[ "$line" =~ ^[[:space:]]*:END:[[:space:]]*$ ]]; then
      break
    elif [[ "$line" =~ ^[[:space:]]*:([A-Za-z_]+):[[:space:]]*(.*)$ ]]; then
      case "${BASH_REMATCH[1]^^}" in
        SESSIONS) plan_sessions=${BASH_REMATCH[2]} ;;
        WORKTREES) plan_worktrees=${BASH_REMATCH[2]} ;;
        OWNERS) plan_owners=${BASH_REMATCH[2]} ;;
      esac
    fi
  done <"$1"
}

# linked_worktree DIR: whether DIR is in a linked worktree, not a main
# checkout, where the operator's own work is.
linked_worktree() {
  local dirs
  dirs=$(git_read -C "$1" rev-parse --path-format=absolute --git-dir --git-common-dir 2>/dev/null) || return 1
  [[ "${dirs%%$'\n'*}" != "${dirs#*$'\n'}" ]]
}

# sole_file FILE: whether FILE is a regular file, not a link, with no other
# name, as agent-plan requires of a plan's files.
sole_file() { [[ -f "$1" && ! -L "$1" && "$(stat -c %h -- "$1")" == 1 ]]; }

# add_plan_trees: adds to plan_trees, when plan_sessions holds this
# session, each existing linked worktree of plan_worktrees whose owner in
# owners is main or none.
add_plan_trees() {
  local value values trees tree owner
  read -ra values <<<"$plan_sessions"
  for value in "${values[@]}"; do
    if [[ "$value" == "claude:$CLAUDE_CODE_SESSION_ID" ]]; then
      read -ra trees <<<"$plan_worktrees"
      for tree in "${trees[@]}"; do
        tree=$(realpath -e -- "$tree" 2>/dev/null) || continue
        owner=${owners[$(fold "$tree")]-main}
        if [[ "$owner" == main ]] && linked_worktree "$tree"; then plan_trees+=("$tree"); fi
      done
      return
    fi
  done
}

# This session's own scratch directory, named by its ID under the scratch
# base; another session's beside it is not this one's to remove. And the
# worktrees of this host's plans, and parts of plans, that name this
# session, such as its task worktrees while the working directory is the
# main checkout; another host's plans name its own sessions.
session=''
plan_trees=()
if [[ "${CLAUDE_CODE_SESSION_ID:-}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  for candidate in "$scratch_base"/*/"$CLAUDE_CODE_SESSION_ID"; do
    if [[ -d "$candidate" ]]; then session=$(realpath -e -- "$candidate"); fi
  done
  # This host's plans, newest first, as plans.Config's ownPlans lists them:
  # a plan's own file with its log beside it, by name with an old-form
  # YYYY-MM-DD read as YYYYMMDD, in descending byte order.
  own_plans=()
  while IFS=$'\t' read -r _ plan; do own_plans+=("$plan"); done < <(
    for file in "$plans_dir"/*."$plans_host".org; do
      name=${file##*/}
      stem=${name%."$plans_host".org}
      if [[ "$stem" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{14})-[a-z0-9]+(-[a-z0-9]+)*$ ]] &&
        sole_file "$file" && sole_file "${file%.org}.log.org"; then
        if [[ "$name" =~ ^([0-9]{4})-([0-9]{2})-([0-9]{2})- ]]; then
          name=${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}-${name:11}
        fi
        printf '%s\t%s\n' "$name" "$file"
      fi
    done | LC_ALL=C sort -r
  )
  # Each worktree's owner, mirroring plans.Config.Owners, which the guard
  # holds agents to: the OWNERS pairs of every plan of this host, the
  # newest plan's owner of a worktree winning. A worktree it gives an agent
  # other than main, the session's own, is that agent's alone.
  declare -A owners=()
  for plan in "${own_plans[@]}"; do
    read_plan "$plan"
    read -ra pairs <<<"$plan_owners"
    for pair in "${pairs[@]}"; do
      if [[ "$pair" == *=* ]]; then
        key=$(fold "$(realpath -m -- "${pair%%=*}")")
        if [[ -z "${owners[$key]+set}" ]]; then owners[$key]=${pair#*=}; fi
      fi
    done
  done
  for plan in "${own_plans[@]}"; do
    for candidate in "$plan" "${plan%.org}".P[0-9]*.org; do
      if sole_file "$candidate"; then
        read_plan "$candidate"
        add_plan_trees
      fi
    done
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
  "$allowed" || refuse "$arg is outside the working directory worktree, the linked worktrees this session's plans name and give no other agent, and this session's scratch directory"
  paths+=("$path")
done

# Validate the entire argument list before the first move to Trash. macOS's
# trash reads -- as a file name, and every path is absolute, so none starts
# with -.
exec "$trash_command" "${paths[@]}"
