# Runs mcp-proxy-restart ($restart) with a fake launchctl, since the real
# one would act on the operator's running agents. The fake's kickstart
# fails as launchctl's does for a service loaded in no domain (113), and
# its bootstrap exits $bootstrap_exit. $plist is the plist the restart must
# load; the agent is in the gui domain.
set -euo pipefail
test_root=$(mktemp -d)
mkdir "$test_root/bin"
printf '#!%s\n' "$BASH" >"$test_root/bin/launchctl"
cat >>"$test_root/bin/launchctl" <<'EOF'
printf '%s\n' "$*" >>"$calls"
case "$1" in
  kickstart) echo "Could not find service" >&2; exit 113 ;;
  bootstrap) exit "$bootstrap_exit" ;;
esac
EOF
chmod +x "$test_root/bin/launchctl"
export PATH="$test_root/bin:$PATH" calls="$test_root/calls"
domain="gui/$(id -u)"

bootstrap_exit=0 "$restart" || { echo "restart failed with bootstrap succeeding" >&2; exit 1; }
expected=$(printf 'kickstart -k %s/org.nix-community.home.mcp-proxy\nbootstrap %s %s' "$domain" "$domain" "$plist")
actual=$(cat "$calls")
[[ "$actual" == "$expected" ]] || {
  printf 'unloaded agent: expected launchctl calls\n%s\ngot\n%s\n' "$expected" "$actual" >&2
  exit 1
}

rm "$calls"
if bootstrap_exit=5 "$restart"; then
  echo "restart succeeded although bootstrap failed" >&2
  exit 1
fi
