#!/usr/bin/env bash
# Runs the ECA server under bubblewrap with a local intercepting proxy, for
# hosts that cannot run the Gondolin backend.
#
# This tier is weaker than Gondolin and says so on every start. The filesystem
# boundary is enforced by bubblewrap, but the egress boundary is only as good
# as the server's willingness to honour $HTTPS_PROXY: an unprivileged
# namespace cannot route traffic without a veth pair, so the alternatives are
# the host's network or no network at all. Anything that clears the proxy
# variables reaches the network directly.

set -euo pipefail

workspace="${ECA_SANDBOX_WORKSPACE:-$PWD}"
config="${ECA_SANDBOX_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/eca}"
state="${ECA_SANDBOX_STATE:-${XDG_CACHE_HOME:-$HOME/.cache}/eca-bwrap}"
log="${ECA_SANDBOX_LOG:-}"
eca="${ECA_SANDBOX_ECA:-eca}"
allow="${ECA_SANDBOX_ALLOW_HOSTS:-models.dev}"
command=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        --) shift; command=("$@"); break ;;
        --workspace) workspace="$2"; shift 2 ;;
        --config) config="$2"; shift 2 ;;
        --state) state="$2"; shift 2 ;;
        --log) log="$2"; shift 2 ;;
        --eca) eca="$2"; shift 2 ;;
        --allow-host) allow="$allow,$2"; shift 2 ;;
        # Accepted so that one .dir-locals.el serves every backend; this one
        # has no guest image to select.
        --image|--guest-path) shift 2 ;;
        *) printf 'eca-bwrap: unknown option %s\n' "$1" >&2; exit 2 ;;
    esac
done

workspace="$(cd "$workspace" && pwd)"

# bubblewrap cannot create a mount point underneath a read-only bind of /, so
# both ends have to exist on the host before the sandbox is built.
mkdir -p "$state" "$HOME/.cache/eca"

# Always reachable, because the server fetches its model catalog on startup.
allow="$(printf '%s\n' "${allow//,/$'\n'}" | awk 'NF && !seen[$0]++' | paste -sd, -)"

# Kept out of /tmp because the sandbox replaces that with a tmpfs, which would
# hide the CA the server has to trust. Living alongside the other state also
# keeps the CA stable between sessions.
confdir="$state/proxy"
proxy_log="$confdir/mitmdump.log"

mkdir -p "$confdir"

cleanup() {
    [[ -n "${proxy_pid:-}" ]] && kill "$proxy_pid" 2>/dev/null || true
}

trap cleanup EXIT INT TERM HUP

# Port 0 lets the kernel pick, but mitmdump does not report the choice, so a
# fixed-but-unused port is found first.
port="$(
    "@python@" - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"

ECA_SANDBOX_ALLOW_HOSTS="$allow" ECA_SANDBOX_LOG="$log" \
    "@mitmdump@" \
    --listen-host 127.0.0.1 \
    --listen-port "$port" \
    --set confdir="$confdir" \
    -s "@allowlist@" \
    >"$proxy_log" 2>&1 &
proxy_pid=$!

ca="$confdir/mitmproxy-ca-cert.pem"

for _ in $(seq 1 100); do
    [[ -r "$ca" ]] && break
    sleep 0.1
done

if [[ ! -r "$ca" ]]; then
    printf 'eca-bwrap: proxy failed to start; see %s\n' "$proxy_log" >&2
    exit 1
fi

printf 'eca-bwrap: egress is proxy-enforced and bypassable; allowed hosts: %s\n' "$allow" >&2

bwrap_args=(
    --ro-bind / /
    --dev /dev
    --proc /proc
    --tmpfs /tmp
    --bind "$workspace" "$workspace"
    --bind "$state" "$HOME/.cache/eca"
    --die-with-parent
    --chdir "$workspace"
    # Set here rather than inherited so that a project's configuration cannot
    # quietly widen them.
    --setenv HTTPS_PROXY "http://127.0.0.1:$port"
    --setenv HTTP_PROXY "http://127.0.0.1:$port"
    --setenv https_proxy "http://127.0.0.1:$port"
    --setenv http_proxy "http://127.0.0.1:$port"
    --setenv no_proxy ""
    --setenv NO_PROXY ""
    # The config-file equivalent does not cover the server's startup traffic;
    # this variable does.
    --setenv SSL_CERT_FILE "$ca"
    --setenv NODE_EXTRA_CA_CERTS "$ca"
)

if [[ -d "$config" ]]; then
    bwrap_args+=(--ro-bind "$config" "$HOME/.config/eca")
fi

if [[ ${#command[@]} -eq 0 ]]; then
    command=("$eca" server)
fi

exec "@bwrap@" "${bwrap_args[@]}" -- "${command[@]}"
