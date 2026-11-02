"""Check generated permissions with native Codex on macOS, outside a sandbox.

From the repository root, generate the policy and run the fixture:
    nix eval --json .#darwinConfigurations.ngrok.config.home-manager.users.wgn.wgn.home.codex.sandbox > /private/tmp/codex-sandbox.json
    python3 modules/home-manager/codex/sandbox-tests.py /private/tmp/codex-sandbox.json

The fixture reads only its temporary files. Run it again after a Codex upgrade;
the Nix regex assertions cannot detect differences in Seatbelt matching.
"""

import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def toml(value):
    if isinstance(value, dict):
        return "{" + ",".join(json.dumps(k) + "=" + toml(v) for k, v in value.items()) + "}"
    return json.dumps(value)


def check(config_path):
    if sys.platform != "darwin":
        raise SystemExit("Run this native Seatbelt check on macOS.")
    codex = shutil.which("codex")
    if not codex:
        raise SystemExit("Install Codex before running this check.")
    config = json.loads(Path(config_path).read_text())
    profile = copy.deepcopy(config["permissions"][config["default_permissions"]])
    profile["network"] = {"enabled": False}
    source = "/private/var/folders/"
    patterns = {
        path: access
        for path, access in profile["filesystem"].items()
        if path.startswith(source) and "/T/secrets.d/" in path
    }
    assert patterns and set(patterns.values()) == {"deny"}, "Missing generated SOPS denies"

    with tempfile.TemporaryDirectory(prefix="codex-sandbox-test-", dir="/private/tmp") as temporary:
        root = Path(temporary)
        secrets = root / "folders/a/b/T/secrets.d"
        cases = []
        for generation, readable in [
            ("1", True), ("111", True), ("9876543210", True),
            ("1" * 20, True), ("1" * 21, False), ("1" * 30, False),
            ("x1", False), ("12x3", False), ("1" * 19 + "x", False),
            ("latest", False),
        ]:
            cases.append((secrets / generation / "rendered/git.inc", readable))
        for name in [
            "age-keys.txt", "111/notes-deploy-key", "111/rendered/ssh.inc",
            "111/rendered/git.inc.bak", "111/rendered/git.in", "111/renderedx/git.inc",
        ]:
            cases.append((secrets / name, False))
        for path, _ in cases:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("sandbox fixture\n")
        alias = root / "config/sops-nix/secrets"
        alias.parent.mkdir(parents=True)
        alias.symlink_to(secrets / "111", target_is_directory=True)
        cases.extend([
            (alias / "rendered/git.inc", True),
            (alias / "rendered/ssh.inc", False),
            (alias / "notes-deploy-key", False),
        ])
        child_home = root / "codex"
        child_home.mkdir()
        environment = dict(os.environ, CODEX_HOME=str(child_home), TMPDIR=str(root))

        def run():
            result = subprocess.run(
                [codex, "sandbox", "-C", str(root), "-P", "fixture", "-c",
                 "permissions.fixture=" + toml(profile), "--", sys.executable, "-I", "-S", "-c",
                 "import json,sys; result=[]\n"
                 "for path in sys.argv[1:]:\n"
                 " try:\n"
                 "  with open(path, 'rb') as stream: stream.read(1)\n"
                 "  result.append(True)\n"
                 " except PermissionError: result.append(False)\n"
                 "print(json.dumps(result))", *[str(path) for path, _ in cases]],
                env=environment, capture_output=True, text=True,
            )
            if result.returncode:
                raise SystemExit(
                    f"Codex sandbox failed before checking reads (exit {result.returncode}):\n"
                    + result.stderr
                )
            return json.loads(result.stdout)

        baseline = run()
        assert all(baseline), "Baseline cannot read fixture files"
        profile["filesystem"].update({
            path.replace(source, str(root / "folders") + "/", 1): access
            for path, access in patterns.items()
        })
        actual = run()
        failures = [
            f"{path.relative_to(root)}: expected readable={expected}, got {readable}"
            for (path, expected), readable in zip(cases, actual, strict=True)
            if expected != readable
        ]
        assert not failures, "\n".join(failures)
    print("PASS: generated SOPS rules allow Git identity and deny fixture secrets")


if __name__ == "__main__":
    check(sys.argv[1])
