import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest


class SettingsTest(unittest.TestCase):
    def activate(self, home):
        activation = Path(os.environ["activationPath"]).read_text().replace(
            os.environ["operatorHome"], str(home)
        )
        script = home / "activate.sh"
        script.write_text('set -e\nwarnEcho() { echo "$@" >&2; exit 1; }\n' + activation)
        subprocess.run(
            ["bash", str(script)], env=dict(os.environ, HOME=str(home)), check=True
        )

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
                '[features.network_proxy]\n'
                'enabled = false\n'
                'dangerously_allow_all_unix_sockets = true\n'
                '[features.network_proxy.domains]\n'
                '"*" = "allow"\n'
                '[features.network_proxy.unix_sockets]\n'
                '"/var/run/docker.sock" = "allow"\n'
                '[sandbox_workspace_write]\n'
                'writable_roots = ["/operator/root"]\n'
            )
            self.activate(home)
            first = config.read_text()
            settings = tomllib.loads(first)
            self.assertIn("# Keep this comment.", first)
            self.assertEqual(settings["model"], "operator-model")
            self.assertEqual(settings["sandbox_mode"], "workspace-write")
            self.assertIs(settings["sandbox_workspace_write"]["network_access"], True)
            self.assertEqual(settings["features"]["network_proxy"], {
                "enabled": True,
                "allow_local_binding": True,
                "allow_upstream_proxy": False,
                "dangerously_allow_all_unix_sockets": False,
                "dangerously_allow_non_loopback_proxy": False,
                "domains": {"localhost": "allow", "127.0.0.1": "allow", "::1": "allow"},
                "unix_sockets": {"/nix/var/nix/daemon-socket/socket": "allow"},
            })
            cache = home / ".cache/codex/sandbox"
            expected = {
                "GOCACHE": str(cache / "go-build"),
                "GOLANGCI_LINT_CACHE": str(cache / "golangci-lint"),
                "XDG_CACHE_HOME": str(cache / "xdg"),
            }
            environment_settings = settings["shell_environment_policy"]["set"]
            self.assertEqual(environment_settings["KEEP_ME"], "operator-value")
            for name, path in expected.items():
                self.assertEqual(environment_settings.get(name), path)
            roots = settings["sandbox_workspace_write"]["writable_roots"]
            self.assertIn("/operator/root", roots)
            self.assertEqual(roots.count(str(cache)), 1)
            self.activate(home)
            self.assertEqual(config.read_text(), first)

    def test_boolean_proxy_setting(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text('[features]\nnetwork_proxy = true\n')
            self.activate(home)
            proxy = tomllib.loads(config.read_text())["features"]["network_proxy"]
            self.assertIs(proxy["enabled"], True)
            self.assertEqual(proxy["unix_sockets"], {
                "/nix/var/nix/daemon-socket/socket": "allow",
            })


if __name__ == "__main__":
    unittest.main()
