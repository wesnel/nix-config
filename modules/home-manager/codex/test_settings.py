import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest


class SettingsTest(unittest.TestCase):
    def test_caches_preserve_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text(
                '# Keep this comment.\n'
                'model = "operator-model"\n'
                '[shell_environment_policy.set]\n'
                'KEEP_ME = "operator-value"\n'
                '[sandbox_workspace_write]\n'
                'writable_roots = ["/operator/root"]\n'
            )
            activation = Path(os.environ["activationPath"]).read_text().replace(
                os.environ["operatorHome"], directory
            )
            script = home / "activate.sh"
            script.write_text('set -e\nwarnEcho() { echo "$@" >&2; exit 1; }\n' + activation)
            environment = dict(os.environ, HOME=directory)
            subprocess.run(["bash", str(script)], env=environment, check=True)
            first = config.read_text()
            settings = tomllib.loads(first)
            self.assertIn("# Keep this comment.", first)
            self.assertEqual(settings["model"], "operator-model")
            self.assertEqual(settings["sandbox_mode"], "workspace-write")
            self.assertIs(settings["sandbox_workspace_write"]["network_access"], False)
            cache = home / ".cache/codex/sandbox"
            expected = {
                "GOCACHE": str(cache / "go-build"),
                "GOLANGCI_LINT_CACHE": str(cache / "golangci-lint"),
            }
            environment_settings = settings["shell_environment_policy"]["set"]
            self.assertEqual(environment_settings["KEEP_ME"], "operator-value")
            for name, path in expected.items():
                self.assertEqual(environment_settings.get(name), path)
            roots = settings["sandbox_workspace_write"]["writable_roots"]
            self.assertIn("/operator/root", roots)
            self.assertEqual(roots.count(str(cache)), 1)
            subprocess.run(["bash", str(script)], env=environment, check=True)
            self.assertEqual(config.read_text(), first)


if __name__ == "__main__":
    unittest.main()
