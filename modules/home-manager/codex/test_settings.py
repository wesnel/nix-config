import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest


class SettingsTest(unittest.TestCase):
    def activate(self, home):
        activation = (
            Path(os.environ["activationPath"])
            .read_text()
            .replace(os.environ["operatorHome"], str(home))
        )
        script = home / "activate.sh"
        script.write_text(
            'set -e\nwarnEcho() { echo "$@" >&2; exit 1; }\n' + activation
        )
        subprocess.run(
            ["bash", str(script)],
            env=dict(os.environ, HOME=str(home)),
            check=True,
        )

    def test_caches_preserve_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text(
                "# Keep this comment.\n"
                'model = "operator-model"\n'
                'sandbox_mode = "workspace-write"\n'
                "[shell_environment_policy.set]\n"
                'KEEP_ME = "operator-value"\n'
                "[features.network_proxy]\n"
                "enabled = false\n"
                "dangerously_allow_all_unix_sockets = true\n"
                "[features.network_proxy.domains]\n"
                '"*" = "allow"\n'
                "[features.network_proxy.unix_sockets]\n"
                '"/var/run/docker.sock" = "allow"\n'
                "[sandbox_workspace_write]\n"
                'writable_roots = ["/operator/root"]\n'
            )
            self.activate(home)
            first = config.read_text()
            settings = tomllib.loads(first)
            self.assertIn("# Keep this comment.", first)
            self.assertEqual(settings["model"], "operator-model")
            self.assertEqual(settings["default_permissions"], "wgn-workspace")
            self.assertIs(settings["features"]["network_proxy"], True)
            self.assertNotIn("sandbox_mode", settings)
            self.assertNotIn("sandbox_workspace_write", settings)
            profile = settings["permissions"][settings["default_permissions"]]
            self.assertEqual(profile["extends"], ":workspace")
            self.assertEqual(
                profile["network"],
                {
                    "enabled": True,
                    "allow_local_binding": True,
                    "allow_upstream_proxy": False,
                    "dangerously_allow_all_unix_sockets": False,
                    "dangerously_allow_non_loopback_proxy": False,
                    "domains": {
                        "localhost": "allow",
                        "127.0.0.1": "allow",
                        "::1": "allow",
                    },
                    "unix_sockets": {
                        "/nix/var/nix/daemon-socket/socket": "allow"
                    },
                },
            )
            for path in (".ssh", ".gnupg", ".config/gh", ".codex/auth.json"):
                self.assertEqual(
                    profile["filesystem"][str(home / path)], "deny"
                )
            self.assertEqual(
                profile["filesystem"][":workspace_roots"],
                {
                    "**/.env": "deny",
                    "**/.env.*": "deny",
                },
            )
            environment = settings["shell_environment_policy"]
            self.assertFalse(environment["ignore_default_excludes"])
            for name in ("SSH_AUTH_SOCK", "GH_TOKEN", "OPENAI_API_KEY"):
                self.assertEqual(
                    environment["filters"][name.lower()], "exclude"
                )
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
            self.assertIs(roots["/operator/root"], True)
            self.assertIs(roots[str(cache)], True)
            self.activate(home)
            self.assertEqual(config.read_text(), first)

    def test_boolean_proxy_setting(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text("[features]\nnetwork_proxy = true\n")
            self.activate(home)
            settings = tomllib.loads(config.read_text())
            self.assertIs(settings["features"]["network_proxy"], True)
            profile = settings["permissions"][settings["default_permissions"]]
            proxy = profile["network"]
            self.assertIs(proxy["enabled"], True)
            self.assertEqual(
                proxy["unix_sockets"],
                {
                    "/nix/var/nix/daemon-socket/socket": "allow",
                },
            )

    def test_lifecycle_hooks_keep_compaction_and_restore_other_starts(self):
        hooks = json.loads(Path(os.environ["hooksPath"]).read_text())["hooks"]
        starts = {
            entry.get("matcher", ""): entry["hooks"]
            for entry in hooks["SessionStart"]
        }
        self.assertEqual(
            starts["startup|resume|clear|fork"],
            [
                {
                    "type": "command",
                    "command": "agent-plan find --hook --harness codex",
                }
            ],
        )
        self.assertEqual(
            starts["compact"],
            [
                {
                    "type": "command",
                    "command": "agent-plan remind",
                }
            ],
        )
        self.assertEqual(
            hooks["PreToolUse"],
            [
                {
                    "matcher": ".*",
                    "hooks": [
                        {
                            "type": "command",
                            "command": "pr-loop guard --baseline",
                        }
                    ],
                }
            ],
        )

    def test_profile_replaces_broad_grants(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            config.write_text(
                'default_permissions = "wgn-workspace"\n'
                "[permissions.wgn-workspace]\n"
                'extends = ":workspace"\n'
                "[permissions.wgn-workspace.workspace_roots]\n"
                '"/operator/root" = true\n'
                '"/old-cache" = true\n'
                "[permissions.wgn-workspace.filesystem]\n"
                '":root" = "write"\n'
                "[sandbox_workspace_write]\n"
                'writable_roots = ["/ignored-root"]\n'
            )
            state = home / ".local/state/codex/cache-root"
            state.parent.mkdir(parents=True)
            state.write_text("/old-cache\n")
            self.activate(home)
            first = config.read_text()
            profile = tomllib.loads(first)["permissions"]["wgn-workspace"]
            self.assertEqual(profile["extends"], ":workspace")
            self.assertNotIn("/old-cache", profile["workspace_roots"])
            self.assertNotIn("/ignored-root", profile["workspace_roots"])
            self.assertIs(profile["workspace_roots"]["/operator/root"], True)
            self.assertNotIn(":root", profile["filesystem"])
            self.assertEqual(profile["filesystem"][str(home / ".ssh")], "deny")
            self.activate(home)
            self.assertEqual(config.read_text(), first)

    def test_restrictive_profile_requires_explicit_migration(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config = home / ".codex/config.toml"
            config.parent.mkdir()
            original = (
                "[permissions.wgn-workspace]\n"
                'extends = ":read-only"\n'
                "[permissions.wgn-workspace.workspace_roots]\n"
                '"/operator/root" = true\n'
                "[permissions.wgn-workspace.filesystem]\n"
                '":root" = "write"\n'
                '"/operator/private" = "deny"\n'
                "[sandbox_workspace_write]\n"
                'writable_roots = ["/old-cache"]\n'
                "exclude_tmpdir_env_var = true\n"
                "exclude_slash_tmp = true\n"
            )
            config.write_text(original)
            state = home / ".local/state/codex/cache-root"
            state.parent.mkdir(parents=True)
            state.write_text("/old-cache\n")
            with self.assertRaises(subprocess.CalledProcessError):
                self.activate(home)
            self.assertEqual(config.read_text(), original)
            self.assertEqual(state.read_text(), "/old-cache\n")
            self.assertFalse((state.parent / "pending-roots").exists())

    def test_dormant_profile_grants_stay_inactive(self):
        for restricted in (False, True):
            with self.subTest(restricted=restricted):
                with tempfile.TemporaryDirectory() as directory:
                    home = Path(directory)
                    config = home / ".codex/config.toml"
                    config.parent.mkdir()
                    original = (
                        'sandbox_mode = "workspace-write"\n'
                        "[sandbox_workspace_write]\n"
                        'writable_roots = ["/operator/root"]\n'
                        f"exclude_tmpdir_env_var = {str(restricted).lower()}\n"
                        "[permissions.wgn-workspace]\n"
                        'extends = ":workspace"\n'
                        "[permissions.wgn-workspace.workspace_roots]\n"
                        '"/" = true\n'
                    )
                    config.write_text(original)
                    if restricted:
                        with self.assertRaises(subprocess.CalledProcessError):
                            self.activate(home)
                        self.assertEqual(config.read_text(), original)
                        continue
                    self.activate(home)
                    first = config.read_text()
                    profile = tomllib.loads(first)["permissions"][
                        "wgn-workspace"
                    ]
                    self.assertNotIn("/", profile["workspace_roots"])
                    self.assertIs(
                        profile["workspace_roots"]["/operator/root"], True
                    )
                    self.activate(home)
                    self.assertEqual(config.read_text(), first)


if __name__ == "__main__":
    unittest.main()
