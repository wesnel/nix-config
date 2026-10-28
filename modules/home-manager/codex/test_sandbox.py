import errno
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import tomllib
import urllib.error
import urllib.request


def denied(operation):
    try:
        operation()
    except OSError as error:
        if error.errno not in (errno.EPERM, errno.EACCES):
            raise
    else:
        raise AssertionError("sandbox allowed a forbidden operation")


def inside(root, port, repository):
    denied(lambda: (root / "outside").write_text("probe"))
    denied(lambda: (root / "workspace/.git/probe").write_text("probe"))
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        denied(lambda: connection.connect(str(root / "unlisted.sock")))
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
        connection.settimeout(2)
        denied(lambda: connection.connect(("192.0.2.1", 443)))
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
        connection.settimeout(2)
        denied(lambda: connection.connect(("192.168.1.1", 443)))
    proxy = urllib.request.ProxyHandler({"http": os.environ["HTTP_PROXY"]})
    opener = urllib.request.build_opener(proxy)
    try:
        opener.open("http://example.invalid", timeout=5)
    except urllib.error.HTTPError as error:
        assert error.code == 403, error
    else:
        raise AssertionError("proxy allowed an unlisted domain")
    with socket.create_connection(("127.0.0.1", port), timeout=5) as connection:
        connection.sendall(b"local-test")
        assert connection.recv(10) == b"local-test"
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as server:
        server.bind(("127.0.0.1", 0))
        server.listen()
        with socket.create_connection(server.getsockname(), timeout=5):
            connection, _ = server.accept()
            connection.close()
    subprocess.run(
        ["nix", "build", "--no-link",
         str(repository) + "#checks.aarch64-darwin.agent-names"],
        check=True,
    )
    expression = (
        'builtins.derivation { name = ' + json.dumps(root.name) + '; '
        'system = builtins.currentSystem; builder = "/bin/sh"; '
        'args = ["-c" "printf sandbox-ok > $out"]; }'
    )
    output = subprocess.check_output(
        ["nix", "build", "--no-link", "--print-out-paths", "--impure",
         "--expr", expression],
        text=True,
    ).strip()
    assert Path(output).read_text() == "sandbox-ok"
    subprocess.run(
        ["go", "-C", str(repository / "overlays/agent-tools"),
         "test", "-count=1", "./..."],
        check=True,
    )
    print("PASS: Nix build, Go tests and loopback; "
          "other TCP, Unix sockets, Git and home writes denied")


def run(activation, operator_home, repository):
    module_cache = subprocess.check_output(
        ["go", "env", "GOMODCACHE"], text=True
    ).strip()
    with tempfile.TemporaryDirectory(
        prefix="codex-local-network-", dir=Path.home()
    ) as directory:
        root = Path(directory)
        workspace = root / "workspace"
        workspace.mkdir()
        script = root / "activate.sh"
        script.write_text(
            'set -e\nwarnEcho() { echo "$@" >&2; exit 1; }\n'
            + activation.read_text().replace(operator_home, directory)
        )
        environment = dict(os.environ, HOME=directory)
        subprocess.run(["bash", str(script)], env=environment, check=True)
        config_path = root / ".codex/config.toml"
        settings = tomllib.loads(config_path.read_text())
        # The sandbox CLI requires a named profile; project the interactive
        # workspace permissions into one using the generated proxy policy.
        profile = '\n[permissions.runtime-test]\nextends = ":workspace"\n'
        profile += '[permissions.runtime-test.network]\nenabled = true\n'
        profile += '[permissions.runtime-test.filesystem]\n'
        for path in settings["sandbox_workspace_write"]["writable_roots"]:
            profile += json.dumps(path) + ' = "write"\n'
        config_path.write_text(config_path.read_text() + profile)
        environment.update(settings["shell_environment_policy"]["set"])
        environment["GOMODCACHE"] = module_cache
        environment["CODEX_HOME"] = str(root / ".codex")
        subprocess.run(
            ["git", "init", "-q", str(workspace)],
            env=environment, check=True,
        )
        with (
            socket.socket(socket.AF_INET, socket.SOCK_STREAM) as server,
            socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as unlisted,
        ):
            server.bind(("127.0.0.1", 0))
            server.listen()
            unlisted.bind(str(root / "unlisted.sock"))
            unlisted.listen()

            def echo():
                connection, _ = server.accept()
                with connection:
                    connection.sendall(connection.recv(10))

            threading.Thread(target=echo, daemon=True).start()
            subprocess.run(
                ["codex", "sandbox", "-P", "runtime-test", "-C", str(workspace), "--",
                 sys.executable, str(Path(__file__).resolve()), "inside", directory,
                 str(server.getsockname()[1]), str(repository)],
                env=environment,
                check=True,
            )


if __name__ == "__main__":
    if len(sys.argv) == 5 and sys.argv[1] == "inside":
        inside(Path(sys.argv[2]), int(sys.argv[3]), Path(sys.argv[4]))
    elif len(sys.argv) == 4:
        run(Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]).resolve())
    else:
        raise SystemExit("usage: test_sandbox.py ACTIVATION OPERATOR_HOME REPOSITORY")
