set -euo pipefail
fixture=$(mktemp -d)
export PLAN_TEST_LOG="$fixture/calls"
warnings="$fixture/warnings"
sed -e "s|$agentPlan|$fixtureAgent|g" -e "s|$notesKey|$fixture/key|g" "$runnerPath" >"$fixture/runner"
grep -Fq "$fixtureAgent" "$fixture/runner" || { echo 'runner fixture did not replace agent-plan' >&2; exit 1; }
if grep -Fq "$agentPlan" "$fixture/runner" || grep -Fq "$notesKey" "$fixture/runner"; then
  echo 'runner fixture retained a deployed command or key pathname' >&2
  exit 1
fi
chmod +x "$fixture/runner"
sed "s|$runnerPath|$fixture/runner|g" "$activationPath" >"$fixture/activation"
grep -Fq "$fixture/runner" "$fixture/activation" || { echo 'activation fixture did not replace the runner' >&2; exit 1; }
if grep -Fq "$runnerPath" "$fixture/activation"; then
  echo 'activation fixture retained its deployed runner' >&2
  exit 1
fi
# As Home Manager's run --quiet does, drop stdout and keep stderr, which
# is what the operator sees of the runner at a switch.
run() {
  if [[ "${1-}" == --quiet ]]; then
    shift
    "$@" >/dev/null
  else
    "$@"
  fi
}
warnEcho() { printf '%s\n' "$*" >>"$warnings"; }
: >"$PLAN_TEST_LOG"
source "$fixture/activation" 2>"$fixture/stderr"
[[ ! -s "$PLAN_TEST_LOG" ]] || {
  echo 'activation initialized or synced while its key was missing' >&2
  exit 1
}
[[ ! -s "$warnings" ]] || { echo 'activation failed the deferred sync' >&2; exit 1; }
grep -Fq "$fixture/key" "$fixture/stderr" || {
  echo 'activation did not warn on stderr that the key is missing' >&2
  exit 1
}
: >"$PLAN_TEST_LOG"
"$fixture/runner" --settle 5s 2>"$fixture/stderr"
[[ ! -s "$PLAN_TEST_LOG" ]] || { echo 'watcher runner synced while its key was missing' >&2; exit 1; }
grep -Fq "$fixture/key" "$fixture/stderr" || {
  echo 'watcher runner did not warn on stderr that the key is missing' >&2
  exit 1
}
: >"$PLAN_TEST_LOG"
touch "$fixture/key"
source "$fixture/activation"
[[ "$(cat "$PLAN_TEST_LOG")" == $'init\nsync' ]] || {
  echo 'activation did not init and sync with a readable fixture key' >&2
  exit 1
}
: >"$PLAN_TEST_LOG"
"$fixture/runner" --settle 5s
[[ "$(cat "$PLAN_TEST_LOG")" == $'init\nsync --settle 5s' ]] || {
  echo 'watcher runner did not initialize then forward settle arguments to sync' >&2
  exit 1
}
: >"$PLAN_TEST_LOG"
if PLAN_INIT_EXIT=23 "$fixture/runner" 2>"$fixture/stderr"; then
  echo 'runner hid initialization failure' >&2
  exit 1
else
  [[ $? == 23 ]] || { echo 'runner changed initialization failure status' >&2; exit 1; }
fi
[[ "$(cat "$PLAN_TEST_LOG")" == $'init\nsync' ]] || { echo 'runner did not sync after initialization failed' >&2; exit 1; }
grep -Fq 'init failed' "$fixture/stderr" || { echo 'runner did not warn that initialization failed' >&2; exit 1; }
echo 'activation key-readiness checks passed'
