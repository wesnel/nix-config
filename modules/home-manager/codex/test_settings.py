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
            self.assertNotIn("sandbox_mode", settings)
            self.assertNotIn("sandbox_workspace_write", settings)
            self.assertEqual(settings["default_permissions"], "wgn-workspace")
            profile = settings["permissions"]["wgn-workspace"]
            self.assertEqual(profile["extends"], ":workspace")
            self.assertIs(profile["network"]["enabled"], True)
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
            roots = profile["workspace_roots"]
            self.assertIn("/operator/root", roots)
            self.assertIs(roots[str(cache)], True)
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

    def test_profile_replaces_broad_grants(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text(
                '[permissions.wgn-workspace]\n'
                'extends = ":read-only"\n'
                '[permissions.wgn-workspace.workspace_roots]\n'
                '"/operator/root" = true\n'
                '[permissions.wgn-workspace.filesystem]\n'
                '":root" = "write"\n'
                '"/operator/private" = "deny"\n'
                '[sandbox_workspace_write]\n'
                'writable_roots = ["/old-cache"]\n'
                'exclude_tmpdir_env_var = true\n'
                'exclude_slash_tmp = true\n'
            )
            state = home / ".local/state/codex/cache-root"
            state.parent.mkdir(parents=True)
            state.write_text("/old-cache\n")
            self.activate(home)
            first = config.read_text()
            profile = tomllib.loads(first)["permissions"]["wgn-workspace"]
            self.assertEqual(profile["extends"], ":workspace")
            self.assertNotIn("/old-cache", profile["workspace_roots"])
            self.assertIs(profile["workspace_roots"]["/operator/root"], True)
            self.assertEqual(profile["filesystem"], {
                "/operator/private": "deny",
                ":tmpdir": "read",
                ":slash_tmp": "read",
            })
            self.activate(home)
            self.assertEqual(config.read_text(), first)


if __name__ == "__main__":
    unittest.main()
