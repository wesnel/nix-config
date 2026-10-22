# Runs gh-read, built around a gh that prints what it was given, where it
# ran and what git found there: gh-read-tests GH-READ
# Every case runs, and the failures are counted at the end.
set +o errexit
gh_read=$1
failures=0

# Counts a failure in this shell, and returns non-zero so that a case run
# in a subshell can be counted by its caller.
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
  return 1
}

top=$(mktemp -d)
top=$(cd "$top" && pwd -P)
export HOME=$top/home
mkdir -p "$HOME"
export GIT_CONFIG_NOSYSTEM=1

# repo DIR ORIGIN: a repository at DIR whose origin is ORIGIN, or none.
repo() {
  mkdir -p "$1"
  git -C "$1" init -q
  if [[ -n "$2" ]]; then
    git -C "$1" remote add origin "$2"
  fi
}

refused() {
  local out
  if out=$("$gh_read" "$@" 2>&1); then
    fail "gh-read $* in $PWD ran: $out"
  elif [[ "$out" != "gh-read: refused"* || "$out" == *"args:"* ]]; then
    fail "gh-read $* in $PWD failed without refusing: $out"
  fi
}

# passed WANT ARGS...: gh-read ARGS passes gh WANT, the argument list as
# the fake gh prints it.
passed() {
  local want="args:$1" out
  shift
  if ! out=$("$gh_read" "$@" 2>&1); then
    fail "gh-read $* in $PWD was refused: $out"
    return
  fi
  if ! grep -qxF -- "$want" <<<"$out"; then
    fail "gh-read $* in $PWD passed gh $(grep '^args:' <<<"$out"), not $want"
  fi
}

repo "$top/plain" https://github.com/o/r.git
git -C "$top/plain" symbolic-ref HEAD refs/heads/wgn/topic
cd "$top/plain" || exit 1

# Subcommands other than the reads.
refused api repos/o/r
refused pr create
refused pr merge 1
refused pr
refused auth token
refused repo clone o/r
refused run rerun 1
refused

# Flags that open a browser.
for args in "--web" "-w" "--web=true" "-cw"; do
  # shellcheck disable=SC2086
  refused pr view 1 -R o/r $args
done
refused run view 1 -w
refused repo view o/r --web

# Arguments that name a repository pass unchanged.
passed " [pr] [view] [12] [-R] [o/r]" pr view 12 -R o/r
passed " [pr] [view] [-Ro/r] [1]" pr view -Ro/r 1
passed " [pr] [view] [--repo=o/r] [-c] [1]" pr view --repo=o/r -c 1
passed " [pr] [view] [https://github.com/o/r/pull/1]" pr view https://github.com/o/r/pull/1
passed " [run] [view] [https://github.com/o/r/actions/runs/5]" run view https://github.com/o/r/actions/runs/5
passed " [repo] [view] [o/r] [-b] [wgn/feature/x] [--json] [name]" repo view o/r -b wgn/feature/x --json name
passed " [pr] [view] [1] [-R] [o/r] [--json] [title] [--jq] [.title]" pr view 1 -R o/r --json title --jq .title
passed " [pr] [view] [1] [-R] [o/r] [--json] [title] [-q] [.title]" pr view 1 -R o/r --json title -q .title
passed " [pr] [view] [1] [-R] [o/r] [--json] [title] [--template] [{{.title}}]" pr view 1 -R o/r --json title --template '{{.title}}'

# Otherwise gh-read names the working directory's repository itself.
passed " [pr] [view] [-R] [github.com/o/r] [12]" pr view 12
passed " [pr] [list] [-R] [github.com/o/r] [--state] [open] [-L] [5]" pr list --state open -L 5
passed " [pr] [diff] [-R] [github.com/o/r] [1] [--name-only]" pr diff 1 --name-only
passed " [pr] [checks] [-R] [github.com/o/r] [1] [--watch] [--interval] [5]" pr checks 1 --watch --interval 5
passed " [pr] [status] [-R] [github.com/o/r]" pr status
passed " [run] [list] [-R] [github.com/o/r] [-L] [5] [--branch] [main]" run list -L 5 --branch main
passed " [run] [view] [-R] [github.com/o/r] [123] [--log]" run view 123 --log
passed " [run] [watch] [-R] [github.com/o/r] [123] [--exit-status]" run watch 123 --exit-status
passed " [repo] [view] [-b] [wgn/feature/x] [github.com/o/r]" repo view -b wgn/feature/x

# A pull request command that names no pull request takes the branch
# checked out in the working directory.
passed " [pr] [view] [-R] [github.com/o/r] [wgn/topic]" pr view
passed " [pr] [view] [-R] [github.com/o/r] [--json] [title] [-q] [.title] [wgn/topic]" pr view --json title -q .title
passed " [pr] [checks] [-R] [github.com/o/r] [--watch] [--interval] [5] [wgn/topic]" pr checks --watch --interval 5
passed " [pr] [diff] [-R] [github.com/o/r] [--name-only] [wgn/topic]" pr diff --name-only
passed " [pr] [view] [-R] [o/r] [wgn/topic]" pr view -R o/r
passed " [pr] [view] [-R] [github.com/o/r] [other]" pr view other
passed " [pr] [list] [-R] [github.com/o/r]" pr list
repo "$top/detached" https://github.com/o/r.git
git -C "$top/detached" -c user.name=t -c user.email=t@e -c commit.gpgsign=false commit -q --allow-empty -m x
git -C "$top/detached" checkout -q --detach
(cd "$top/detached" && refused pr view && refused pr checks) || fail "a detached HEAD"
(cd "$top/detached" && passed " [pr] [view] [-R] [github.com/o/r] [7]" pr view 7) || fail "a detached HEAD with a number"

# A switch is no pull request, and a flag's value is none either, short or
# long, alone or in a cluster.
passed " [pr] [view] [-R] [github.com/o/r] [-c] [wgn/topic]" pr view -c
passed " [pr] [view] [-R] [github.com/o/r] [-cq] [.title] [wgn/topic]" pr view -cq .title
passed " [pr] [view] [-R] [github.com/o/r] [-q.title] [wgn/topic]" pr view -q.title
passed " [pr] [checks] [-R] [github.com/o/r] [-i] [5] [--required] [wgn/topic]" pr checks -i 5 --required
passed " [pr] [diff] [-R] [github.com/o/r] [-e] [*.lock] [--patch] [wgn/topic]" pr diff -e '*.lock' --patch
passed " [pr] [diff] [-R] [github.com/o/r] [--color] [never] [wgn/topic]" pr diff --color never
passed " [pr] [diff] [-R] [github.com/o/r] [--color=never] [wgn/topic]" pr diff --color=never
# run list's -w is --workflow, not --web.
passed " [run] [list] [-R] [github.com/o/r] [-w] [ci.yml]" run list -w ci.yml
refused run list --web

# A branch or host that starts with - would reach gh as a flag.
repo "$top/dash" https://github.com/o/r.git
for branch in --web -w --repo=evil.example/o/r; do
  git -C "$top/dash" symbolic-ref HEAD "refs/heads/$branch"
  (cd "$top/dash" && refused pr view) || fail "the branch $branch"
done
repo "$top/dashhost" https://-wb/o/r.git
(cd "$top/dashhost" && refused repo view && refused pr view 1) || fail "a host starting with -"

# The base repository is gh's: the remote `gh repo set-default` marked,
# else upstream, else origin.
repo "$top/fork" https://github.com/me/r.git
git -C "$top/fork" remote add upstream https://github.com/o/r.git
(cd "$top/fork" && passed " [pr] [view] [-R] [github.com/o/r] [12]" pr view 12) || fail "a fork clone"
git -C "$top/fork" remote add other https://github.com/x/r.git
git -C "$top/fork" config remote.other.gh-resolved base
(cd "$top/fork" && passed " [pr] [view] [-R] [github.com/x/r] [12]" pr view 12) || fail "a set default"
n=0
for origin in git@github.com:o/r.git ssh://git@github.com/o/r https://github.com/o/r/ \
  https://user@github.com/o/r.git; do
  n=$((n + 1))
  repo "$top/forms/$n" "$origin"
  (cd "$top/forms/$n" && passed " [pr] [view] [-R] [github.com/o/r] [1]" pr view 1) || fail "origin $origin"
done
repo "$top/ghe" ssh://git@ghe.example.com:2222/team/tool.git
(cd "$top/ghe" && passed " [pr] [view] [-R] [ghe.example.com/team/tool] [1]" pr view 1) || fail "the GHE origin"

# A working directory gh-read cannot name a repository from.
repo "$top/none" ""
(cd "$top/none" && refused pr view 1) || fail "no origin"
repo "$top/local" /srv/git/r.git
(cd "$top/local" && refused pr view 1) || fail "a local origin"
mkdir -p "$top/bare"
(cd "$top/bare" && refused pr view 1) || fail "no repository"

# A repository whose config runs a program whenever git reads its index,
# uses SSH or takes an alias: gh's git finds no repository, so none runs.
marker=$top/marker
cat >"$top/mark" <<EOF
#!/bin/sh
echo "\$0 \$*" >>"$marker"
exit 0
EOF
chmod +x "$top/mark"
repo "$top/hostile" https://github.com/o/r.git
echo x >"$top/hostile/file"
git -C "$top/hostile" add file
git -C "$top/hostile" config core.fsmonitor "$top/mark"
git -C "$top/hostile" config core.sshCommand "$top/mark"
git -C "$top/hostile" config alias.st "!$top/mark"
git -C "$top/hostile" symbolic-ref HEAD refs/heads/topic
# The control: plain git in that repository runs the program.
git -C "$top/hostile" status >/dev/null 2>&1
if [[ -s "$marker" ]]; then
  rm -f "$marker"
  out=$(cd "$top/hostile" && "$gh_read" pr view 1 2>&1)
  if [[ -e "$marker" ]]; then
    fail "gh-read in a repository with a hostile config ran its program: $(cat "$marker")"
  fi
  grep -qxF 'args: [pr] [view] [-R] [github.com/o/r] [1]' <<<"$out" || fail "gh-read in the hostile repository passed gh: $out"
  if grep -q 'git status: found a repository\|^remote: \|git st: ran' <<<"$out"; then
    fail "gh's git found a repository: $out"
  fi
  grep -qxF 'global: /dev/null' <<<"$out" || fail "gh's git reads a global config: $out"
  dir=$(grep '^pwd: ' <<<"$out" | cut -d' ' -f2-)
  [[ "$dir" != "$top/hostile"* ]] || fail "gh ran in the working directory: $dir"
  [[ -n "$dir" && ! -e "$dir" ]] || fail "gh's directory $dir was not removed"
  # Reading the branch there runs nothing either.
  out=$(cd "$top/hostile" && "$gh_read" pr view 2>&1)
  if [[ -e "$marker" ]]; then
    fail "gh-read pr view in a repository with a hostile config ran its program: $(cat "$marker")"
  fi
  grep -qxF 'args: [pr] [view] [-R] [github.com/o/r] [topic]' <<<"$out" || fail "gh-read pr view in the hostile repository passed gh: $out"
else
  fail "the control did not run the hostile config's program, so the test proves nothing"
fi

# Nor does a GIT_DIR the session exported name a repository to gh's git.
out=$(cd "$top/plain" && GIT_DIR="$top/hostile/.git" "$gh_read" pr view 1 2>&1)
if grep -q 'git status: found a repository\|^remote: ' <<<"$out"; then
  fail "gh's git took the session's GIT_DIR: $out"
fi
[[ ! -e "$marker" ]] || fail "gh-read with GIT_DIR ran the hostile program: $(cat "$marker")"

# gh reads with the operator's own config and login, never a GH_* the
# session set.
out=$(cd "$top/plain" && GH_REPO=evil.example/o/r GH_CONFIG_DIR="$top/hostile" GH_HOST=evil.example GH_TOKEN=x \
  "$gh_read" pr view 1 2>&1)
if grep -q '^env: GH_' <<<"$out"; then
  fail "the session's GH_* reached gh: $out"
fi
grep -qxF 'args: [pr] [view] [-R] [github.com/o/r] [1]' <<<"$out" || fail "gh-read with GH_* set passed gh: $out"

rm -rf "$top"
if ((failures > 0)); then
  echo "$failures gh-read tests failed" >&2
  exit 1
fi
echo "gh-read tests passed"
