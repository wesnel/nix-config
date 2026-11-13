"""Check Codex discovers the rules installed by Home Manager.

Run with the codexRules activation text, optionally checking the native loader:
    python3 modules/home-manager/codex/rules-tests.py ACTIVATION_SCRIPT [CODEX]
All activation and Codex state stays in a temporary home.
"""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


def check(activation, codex=None):
    with tempfile.TemporaryDirectory(prefix="codex-rules-test-") as temporary:
        root = Path(temporary)
        home = root / "home"
        codex_home = home / ".codex"
        rules = codex_home / "rules"
        rules.mkdir(parents=True)
        target = rules / "nix.rules"
        generated = root / "old.rules"
        generated.write_text('prefix_rule(pattern=["old-command"], decision="prompt")\n')
        saved = rules / "default.rules"
        saved.write_text('prefix_rule(pattern=["agent-plan"], decision="allow")\n')
        saved_contents = saved.read_bytes()
        environment = {
            "HOME": str(home), "CODEX_HOME": str(codex_home), "TMPDIR": str(root),
            "PATH": os.environ["PATH"], "XDG_CONFIG_HOME": str(home / ".config"),
            "XDG_CACHE_HOME": str(home / ".cache"), "XDG_STATE_HOME": str(home / ".state"),
        }
        for state in ["symlink", "regular", "absent"]:
            if target.exists():
                target.rename(root / f"previous-{state}")
            if state == "symlink":
                target.symlink_to(generated)
            elif state == "regular":
                target.write_text(generated.read_text())
            subprocess.run(["bash", activation], env=dict(environment, DRY_RUN="1"), check=True)
            assert (target.exists() if state != "absent" else not target.exists())
            if state != "absent":
                assert target.read_text() == generated.read_text()
                assert target.is_symlink() == (state == "symlink")
            for _ in range(2):
                subprocess.run(["bash", activation], env=environment, check=True)
                assert target.is_file() and not target.is_symlink(), (
                    "Codex skips symlinked rule files; activation must install a regular nix.rules"
                )
                assert 'pattern=["agent-plan","log"], decision="prompt"' in target.read_text()
                mirror_rules = [
                    line for line in target.read_text().splitlines()
                    if 'pattern=["plan-dash"' in line
                ]
                assert sorted(mirror_rules) == sorted([
                    f'prefix_rule(pattern=["plan-dash","{command}"], decision="allow")'
                    for command in ["snapshot", "claim", "release"]
                ]), "Only snapshot, claim and release may bypass the command sandbox"
                assert target.stat().st_mode & 0o777 == 0o600
                assert saved.read_bytes() == saved_contents, "Activation changed saved approvals"
                assert generated.read_text().startswith('prefix_rule(pattern=["old-command"]')
        print("PASS: activation installs repeatable regular Codex prompt rules")
        if codex:
            result = subprocess.run(
                [codex, "execpolicy", "check", "--rules", str(saved), "--rules", str(target),
                 "--", "agent-plan", "log", "fixture.org", "--item", "S1", "--kind", "started"],
                env=environment, capture_output=True, text=True, check=True,
            )
            assert json.loads(result.stdout)["decision"] == "prompt"
            for arguments, expected in [
                (["snapshot"], "allow"),
                (["snapshot", "--open"], "allow"),
                (["snapshot", "/fixture/plan.org"], "allow"),
                (["claim"], "allow"),
                (["release"], "allow"),
                (["record"], None),
                (["refresh"], None),
                (["remind"], None),
                (["snapshot-other"], None),
                ([], None),
            ]:
                result = subprocess.run(
                    [codex, "execpolicy", "check", "--rules", str(saved),
                     "--rules", str(target), "--", "plan-dash", *arguments],
                    env=environment, capture_output=True, text=True, check=True,
                )
                observed = json.loads(result.stdout).get("decision")
                assert observed == expected, (arguments, observed, expected)
            # A parse warning proves the real directory loader discovered this file.
            target.write_text("prefix_rule(")
            with tempfile.TemporaryFile(mode="w+") as errors:
                process = subprocess.Popen(
                    [codex, "app-server"], cwd=root, env=environment,
                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors, text=True,
                )
                try:
                    messages, _ = process.communicate(
                        json.dumps({"id": 1, "method": "initialize", "params": {
                            "clientInfo": {"name": "rules-fixture", "version": "1"}}}) + "\n"
                        + '{"method":"initialized"}\n'
                        + '{"id":2,"method":"config/read","params":{}}\n', timeout=15,
                    )
                    warnings = [json.loads(line) for line in messages.splitlines()]
                    assert any(m.get("method") == "configWarning" and "Error parsing rules" in
                               m.get("params", {}).get("summary", "") for m in warnings), messages
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.communicate()
            print("PASS: native Codex discovers installed rules and prompts over saved allow")
        else:
            print("SKIP native loader: pass the installed Codex executable as the second argument")


if __name__ == "__main__":
    check(sys.argv[1], sys.argv[2] if len(sys.argv) > 2 else None)
