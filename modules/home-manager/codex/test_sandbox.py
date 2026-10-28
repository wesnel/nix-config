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


def git(repository, *arguments, environment=None):
    return subprocess.check_output(
        ["git", "-C", str(repository), "-c", "commit.gpgsign=false",
         *arguments], env=environment, text=True, stderr=subprocess.STDOUT,
    ).strip()


def git_inside(workspace, label):
    gitdir, common = [Path(path) for path in git(
        workspace, "rev-parse", "--path-format=absolute",
        "--git-dir", "--git-common-dir",
    ).splitlines()]
    denied(lambda: (common / "config").write_text("probe"))
    denied(lambda: (common / "config.worktree").write_text("probe"))
    denied(lambda: (common / "hooks/probe").write_text("probe"))
    denied(lambda: (common / "worktrees/probe").mkdir(parents=True))
    denied(lambda: (common / "modules/probe").mkdir(parents=True))
    denied(lambda: (gitdir / "config.worktree").write_text("probe"))
    denied(lambda: common.rename(workspace / "moved-metadata"))
    denied(lambda: (workspace / ".codex/config.toml").write_text("probe"))
    replacement = workspace / "replacement-config"
    replacement.write_text("probe")
    denied(lambda: replacement.replace(common / "config"))
    denied(lambda: os.link(common / "config", workspace / "config-alias"))
    base = git(workspace, "branch", "--show-current")
    branch = "feature-" + label
    git(workspace, "switch", "-c", branch)
    (workspace / "tracked").write_text("feature-" + label + "\n")
    git(workspace, "add", "tracked")
    git(workspace, "commit", "-qm", "Fixture feature")
    git(workspace, "switch", base)
    (workspace / "other").write_text("base-" + label + "\n")
    git(workspace, "add", "other")
    git(workspace, "commit", "-qm", "Fixture base")
    git(workspace, "switch", branch)
    git(workspace, "rebase", base)
    git(workspace, "merge-base", "--is-ancestor", base, "HEAD")
    print("PASS: " + label + " Git state writes; configuration, hooks, "
          "other worktrees and metadata replacement denied")


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
    git_setup = os.environ.get("CODEX_GIT_SETUP", "")
    if not git_setup or not Path(git_setup).is_file():
        raise RuntimeError("run this check through nix run .#codex-sandbox-check")
    module_cache = subprocess.check_output(
        ["go", "env", "GOMODCACHE"], text=True
    ).strip()
    with (
        tempfile.TemporaryDirectory(
            prefix="codex-local-network-", dir=Path.home()
        ) as directory,
        tempfile.TemporaryDirectory(
            prefix="c-", dir="/var/tmp"
        ) as temporary_directory,
    ):
        root = Path(directory)
        workspace = root / "workspace"
        workspace.mkdir()
        temporary = Path(temporary_directory)
        script = root / "activate.sh"
        script.write_text(
            'set -e\nwarnEcho() { echo "$@" >&2; exit 1; }\n'
            + activation.read_text().replace(operator_home, directory)
        )
        environment = dict(os.environ, HOME=directory, TMPDIR=str(temporary))
        for name in (
            "SSH_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO",
            "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT",
        ):
            environment.pop(name, None)
        environment.update(
            GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1",
            GIT_AUTHOR_NAME="Sandbox Test",
            GIT_AUTHOR_EMAIL="sandbox@example.invalid",
            GIT_COMMITTER_NAME="Sandbox Test",
            GIT_COMMITTER_EMAIL="sandbox@example.invalid",
        )
        subprocess.run(["bash", str(script)], env=environment, check=True)
        config_path = root / ".codex/config.toml"
        settings = tomllib.loads(config_path.read_text())
        profile = settings["default_permissions"]
        for path, enabled in settings["permissions"][profile]["workspace_roots"].items():
            owned = Path(path)
            if enabled and owned.is_relative_to(root):
                owned.mkdir(parents=True, exist_ok=True)
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
                ["codex", "sandbox", "-P", profile, "-C", str(workspace), "--",
                 sys.executable, str(Path(__file__).resolve()), "inside", directory,
                 str(server.getsockname()[1]), str(repository)],
                env=environment,
                check=True,
            )
        (workspace / "tracked").write_text("seed\n")
        git(workspace, "add", "tracked", environment=environment)
        git(workspace, "commit", "-qm", "Fixture seed", environment=environment)
        for label in ("plain", "linked"):
            current = workspace
            if label == "linked":
                current = root / "linked"
                git(workspace, "worktree", "add", "-q", "-b", "linked-base",
                    str(current), environment=environment)
            subprocess.run(
                [git_setup, str(current)],
                env=environment, check=True,
            )
            project = tomllib.loads((current / ".codex/config.toml").read_text())
            with config_path.open("a") as stream:
                stream.write(
                    '\n[projects.' + json.dumps(str(current))
                    + ']\ntrust_level = "trusted"\n'
                )
            subprocess.run(
                ["codex", "sandbox", "-P", project["default_permissions"],
                 "-C", str(current), "--", sys.executable,
                 str(Path(__file__).resolve()), "git-inside", str(current), label],
                env=environment, check=True,
            )


if __name__ == "__main__":
    if len(sys.argv) == 5 and sys.argv[1] == "inside":
        inside(Path(sys.argv[2]), int(sys.argv[3]), Path(sys.argv[4]))
    elif len(sys.argv) == 4 and sys.argv[1] == "git-inside":
        git_inside(Path(sys.argv[2]), sys.argv[3])
    elif len(sys.argv) == 4:
        run(Path(sys.argv[1]), sys.argv[2], Path(sys.argv[3]).resolve())
    else:
        raise SystemExit("usage: test_sandbox.py ACTIVATION OPERATOR_HOME REPOSITORY")
