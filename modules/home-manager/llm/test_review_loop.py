import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

PROGRAM = sys.argv.pop(1)


class CodexPreflightTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.calls = self.root / "calls.jsonl"
        fake = self.root / "codex"
        # A model CLI cannot run in tests. The fake executes only the fixed
        # preflight command so its exact marker predicate remains exercised.
        fake.write_text(
            f"#!{sys.executable}\n"
            "import json, os, subprocess, sys\n"
            "args = sys.argv[1:]\n"
            "with open(os.environ['TEST_CALLS'], 'a') as calls:\n"
            "    calls.write(json.dumps(args) + '\\n')\n"
            "if 'sandbox' in args:\n"
            "    env = dict(os.environ)\n"
            "    if 'TEST_SESSION' in env:\n"
            "        env['PR_LOOP_SESSION'] = env['TEST_SESSION']\n"
            "    if 'TEST_MODE' in env:\n"
            "        env['PR_LOOP_MODE'] = env['TEST_MODE']\n"
            "    if 'TEST_UNAVAILABLE' in env:\n"
            "        sys.exit(17)\n"
            "    command = args[args.index('--') + 1:]\n"
            "    sys.exit(subprocess.run(command, env=env).returncode)\n"
            "sys.exit(0)\n"
        )
        fake.chmod(0o700)
        self.environment = dict(
            os.environ,
            PATH=str(self.root) + os.pathsep + os.environ["PATH"],
            HOME=str(self.root),
            CODEX_HOME=str(self.root / ".codex"),
            XDG_CONFIG_HOME=str(self.root / ".config"),
            XDG_STATE_HOME=str(self.root / ".local/state"),
            XDG_CACHE_HOME=str(self.root / ".cache"),
            TEST_CALLS=str(self.calls),
        )
        for key in ("TEST_SESSION", "TEST_MODE", "TEST_UNAVAILABLE"):
            self.environment.pop(key, None)

    def launch(self, mode="bots", **overrides):
        self.calls.write_text("")
        result = subprocess.run(
            ["bash", PROGRAM, "--harness", "codex", "--" + mode, "12", "#13"],
            env=dict(self.environment, **overrides),
            capture_output=True,
            text=True,
        )
        calls = [
            json.loads(line) for line in self.calls.read_text().splitlines()
        ]
        return result, calls

    def test_same_profile_and_marker_settings_in_both_stages(self):
        for mode in ("bots", "people"):
            with self.subTest(mode=mode):
                result, calls = self.launch(mode)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(calls), 2, calls)
                settings = [
                    "--no-daemon",
                    "-c",
                    'default_permissions="wgn-project-git"',
                    "-c",
                    'shell_environment_policy.set.PR_LOOP_SESSION="1"',
                    "-c",
                    f'shell_environment_policy.set.PR_LOOP_MODE="{mode}"',
                ]
                self.assertEqual(calls[0][:7], settings)
                self.assertEqual(
                    calls[0][7:11],
                    [
                        "sandbox",
                        "-P",
                        "wgn-project-git",
                        "--",
                    ],
                )
                self.assertEqual(calls[1][:-1], settings)
                self.assertIn(
                    f"{mode} review loop on PRs: 12 #13.", calls[1][-1]
                )

    def test_failed_preflight_never_starts_session(self):
        for overrides in (
            {"TEST_SESSION": ""},
            {"TEST_SESSION": "0"},
            {"TEST_MODE": ""},
            {"TEST_MODE": "people"},
            {"TEST_UNAVAILABLE": "1"},
        ):
            with self.subTest(overrides=overrides):
                result, calls = self.launch(**overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(len(calls), 1, calls)
                self.assertIn("sandbox", calls[0])
                self.assertIn("preflight", result.stderr)
                self.assertIn("trust", result.stderr)
                self.assertIn("filter", result.stderr)


if __name__ == "__main__":
    unittest.main()
