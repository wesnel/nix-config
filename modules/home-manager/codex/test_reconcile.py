import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import tomllib
import unittest
from unittest.mock import patch


class ReconcileTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name)
        self.config = self.home / ".codex/config.toml"
        self.config.parent.mkdir()
        self.state = self.home / "state"
        self.state.mkdir()
        self.roots = {
            "plans": str(self.home / "new-plans"),
            "cache": str(self.home / "new-cache"),
        }
        self.proxy = {
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
            "unix_sockets": {"/nix/var/nix/daemon-socket/socket": "allow"},
        }
        self.settings = {
            "default_permissions": "managed",
            "permissions": {
                "managed": {
                    "extends": ":workspace",
                    "filesystem": {str(self.home / "protected"): "deny"},
                    "network": self.proxy,
                }
            },
            "features": {"network_proxy": True},
            "shell_environment_policy": {
                "ignore_default_excludes": False,
                "filters": {"SSH_AUTH_SOCK": "exclude"},
                "set": {"GOCACHE": str(self.home / "new-cache/go-build")},
            },
            "notice": {"hide_rate_limit_model_nudge": True},
        }
        self.program = os.environ.get(
            "reconcileProgram", str(Path(__file__).with_name("reconcile.py"))
        )

    def reconcile(self, success=True):
        result = subprocess.run(
            [
                sys.executable,
                self.program,
                str(self.config),
                json.dumps(self.settings),
                json.dumps(self.roots),
                str(self.state),
            ],
            env=dict(
                os.environ, HOME=str(self.home), PYTHONDONTWRITEBYTECODE="1"
            ),
            capture_output=True,
            text=True,
        )
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def test_migration_preserves_operator_settings_and_replaces_policy(self):
        old_plans = str(self.home / "old-plans")
        old_cache = str(self.home / "old-cache")
        user_root = str(self.home / "operator-root")
        self.config.write_text(
            "# Keep this comment.\n"
            'model = "operator-model"\n'
            'sandbox_mode = "workspace-write"\n'
            "[sandbox_workspace_write]\n"
            "writable_roots = "
            f"{json.dumps([old_plans, old_cache, user_root])}\n"
            "network_access = true\n"
            "[shell_environment_policy.set]\n"
            'KEEP_ME = "operator-value"\n'
            "[features.network_proxy.domains]\n"
            '"*" = "allow"\n'
            "[permissions.managed.filesystem]\n"
            '"/broad" = "write"\n'
            "[permissions.managed.network.domains]\n"
            '"*" = "allow"\n'
        )
        (self.state / "plans-root").write_text(old_plans + "\n")
        (self.state / "cache-root").write_text(old_cache + "\n")
        self.reconcile()
        first = self.config.read_text()
        settings = tomllib.loads(first)
        self.assertNotIn("sandbox_mode", settings)
        self.assertNotIn("sandbox_workspace_write", settings)
        self.assertIn("# Keep this comment.", first)
        self.assertEqual(settings["model"], "operator-model")
        self.assertEqual(settings["default_permissions"], "managed")
        profile = settings["permissions"]["managed"]
        self.assertEqual(
            profile["filesystem"], {str(self.home / "protected"): "deny"}
        )
        self.assertEqual(profile["network"], self.proxy)
        self.assertIs(settings["features"]["network_proxy"], True)
        self.assertEqual(
            profile["workspace_roots"],
            {
                user_root: True,
                self.roots["plans"]: True,
                self.roots["cache"]: True,
            },
        )
        self.assertEqual(
            settings["shell_environment_policy"]["set"],
            {
                "KEEP_ME": "operator-value",
                "GOCACHE": str(self.home / "new-cache/go-build"),
            },
        )
        for name, root in self.roots.items():
            self.assertEqual(
                (self.state / (name + "-root")).read_text(), root + "\n"
            )
        self.reconcile()
        self.assertEqual(self.config.read_text(), first)

    def test_interrupted_root_change_does_not_preserve_abandoned_grants(self):
        for stop in (
            "before-config",
            "pending-roots",
            "config.toml",
            "plans-root",
            "cache-root",
        ):
            with self.subTest(stop=stop):
                user_root = str(self.home / "operator-root")
                self.config.write_text(
                    "[sandbox_workspace_write]\n"
                    f"writable_roots = {json.dumps([user_root])}\n"
                )
                self.roots = {
                    key: str(self.home / ("generation-a-" + key))
                    for key in ("plans", "cache")
                }
                self.reconcile()
                module = runpy.run_path(self.program)
                reconcile = module["reconcile"]
                write = module["atomic_write"]

                def interrupt(path, text):
                    if stop == "before-config" and path == self.config:
                        raise OSError(
                            "simulated crash before configuration write"
                        )
                    write(path, text)
                    if path.name == stop:
                        raise OSError("simulated crash after " + stop)

                self.roots = {
                    key: str(self.home / ("generation-b-" + key))
                    for key in self.roots
                }
                with patch.dict(reconcile.__globals__, atomic_write=interrupt):
                    with self.assertRaisesRegex(OSError, "simulated crash"):
                        reconcile(
                            self.config, self.settings, self.roots, self.state
                        )
                self.roots = {
                    key: str(self.home / ("generation-c-" + key))
                    for key in self.roots
                }
                self.reconcile()
                current = tomllib.loads(self.config.read_text())
                self.assertEqual(
                    current["permissions"]["managed"]["workspace_roots"],
                    {
                        user_root: True,
                        **{root: True for root in self.roots.values()},
                    },
                )
                self.assertEqual(
                    (self.state / "pending-roots").read_text(), ""
                )
                stable = self.config.read_text()
                self.reconcile()
                self.assertEqual(self.config.read_text(), stable)

    def test_conflicting_environment_is_rejected_without_writes_or_values(
        self,
    ):
        for name in (
            "SSH_AUTH_SOCK",
            "ssh_auth_sock",
            "CUSTOM_TOKEN",
            "other_secret",
        ):
            with self.subTest(name=name):
                original = (
                    "[shell_environment_policy.set]\n"
                    + name
                    + ' = "synthetic-value"\n'
                )
                self.config.write_text(original)
                before = {
                    p.name: p.read_bytes()
                    for p in self.state.iterdir()
                    if p.name != "reconcile.lock"
                }
                result = self.reconcile(success=False)
                self.assertIn("environment", result.stderr)
                self.assertNotIn("synthetic-value", result.stderr)
                self.assertEqual(self.config.read_text(), original)
                self.assertEqual(
                    {
                        p.name: p.read_bytes()
                        for p in self.state.iterdir()
                        if p.name != "reconcile.lock"
                    },
                    before,
                )

    def test_conflicting_roots_are_rejected_before_config_or_state_writes(
        self,
    ):
        self.settings["permissions"]["managed"]["filesystem"].update(
            {
                str(self.home / "restricted-*/data"): "deny",
                ":workspace_roots": {"**/protected-name": "deny"},
            }
        )
        alias = self.home / "alias"
        alias.symlink_to(self.home / "protected")
        conflicts = [
            self.home / "protected",
            self.home / "protected/child",
            alias,
            self.home / "restricted-one/data/child",
            self.home / "project/protected-name/child",
        ]
        for root in conflicts:
            with self.subTest(root=root):
                original = (
                    "[sandbox_workspace_write]\n"
                    + f"writable_roots = {json.dumps([str(root)])}\n"
                )
                self.config.write_text(original)
                result = self.reconcile(success=False)
                self.assertIn("writable root", result.stderr)
                self.assertEqual(self.config.read_text(), original)
                self.assertFalse((self.state / "pending-roots").exists())
                self.assertFalse((self.state / "plans-root").exists())

    def test_legacy_environment_filters_migrate_without_widening(self):
        self.config.write_text(
            "[shell_environment_policy]\n"
            'exclude = ["OLD_PRIVATE"]\n'
            'include_only = ["PATH", "GOCACHE"]\n'
        )
        self.reconcile()
        policy = tomllib.loads(self.config.read_text())[
            "shell_environment_policy"
        ]
        self.assertNotIn("exclude", policy)
        self.assertNotIn("include_only", policy)
        self.assertEqual(
            policy["filters"],
            {
                "old_private": "exclude",
                "path": "include",
                "gocache": "include",
                "ssh_auth_sock": "exclude",
            },
        )
        self.assertFalse(policy["ignore_default_excludes"])

    def test_environment_filter_keys_follow_runtime_lowercase(self):
        cases = (
            (
                "[shell_environment_policy]\n"
                'exclude = ["ssh_auth_sock", "SSH_AUTH_SOCK"]\n',
                {"ssh_auth_sock": "exclude"},
            ),
            (
                "[shell_environment_policy.filters]\n"
                'ssh_auth_sock = "exclude"\n',
                {"ssh_auth_sock": "exclude"},
            ),
            (
                '[shell_environment_policy]\nexclude = ["Ä_ENV", "ä_env"]\n',
                {"ä_env": "exclude", "ssh_auth_sock": "exclude"},
            ),
            (
                "[shell_environment_policy.filters]\n"
                '"ß_ENV" = "include"\n"SS_ENV" = "exclude"\n',
                {
                    "ß_env": "include",
                    "ss_env": "exclude",
                    "ssh_auth_sock": "exclude",
                },
            ),
        )
        for original, expected in cases:
            with self.subTest(original=original):
                self.config.write_text(original)
                self.reconcile()
                first = self.config.read_text()
                filters = tomllib.loads(first)["shell_environment_policy"][
                    "filters"
                ]
                self.assertEqual(filters, expected)
                self.assertEqual(
                    len(filters), len({p.lower() for p in filters})
                )
                self.reconcile()
                self.assertEqual(self.config.read_text(), first)

    def test_environment_allowlist_conflicts_leave_config_and_state_unchanged(
        self,
    ):
        for original in (
            "[shell_environment_policy]\n"
            'include_only = ["ONLY_THIS"]\nexclude = ["ONLY_THIS"]\n',
            "[shell_environment_policy]\n"
            'include_only = ["Ä_ENV"]\nexclude = ["ä_env"]\n',
            '[shell_environment_policy]\ninclude_only = ["ssh_auth_sock"]\n',
            '[shell_environment_policy.filters]\nssh_auth_sock = "include"\n',
            "[shell_environment_policy.filters]\n"
            'ONLY_THIS = "include"\nonly_this = "exclude"\n',
            "[shell_environment_policy]\n"
            'include_only = ["PATH", "SSH_AUTH_SOCK"]\n',
            "[shell_environment_policy]\n"
            'include_only = ["PATH", "ONLY_THIS"]\nexclude = ["only_this"]\n'
            '[shell_environment_policy.set]\nONLY_THIS = "synthetic-value"\n',
        ):
            with self.subTest(original=original):
                self.config.write_text(original)
                for key in self.roots:
                    (self.state / (key + "-root")).write_text(
                        str(self.home / ("old-" + key)) + "\n"
                    )
                (self.state / "pending-roots").write_text(
                    json.dumps([str(self.home / "abandoned-plans")])
                )
                before = {
                    p.name: p.read_bytes()
                    for p in self.state.iterdir()
                    if p.name != "reconcile.lock"
                }
                result = self.reconcile(success=False)
                self.assertIn("environment", result.stderr)
                self.assertNotIn("synthetic-value", result.stderr)
                self.assertEqual(self.config.read_text(), original)
                self.assertEqual(
                    {
                        p.name: p.read_bytes()
                        for p in self.state.iterdir()
                        if p.name != "reconcile.lock"
                    },
                    before,
                )

    def test_environment_preserves_nonconflicting_includes_and_explicit_values(
        self,
    ):
        for original in (
            "[shell_environment_policy]\n"
            'include_only = ["PATH", "CUSTOM"]\nexclude = ["PRIVATE"]\n',
            "[shell_environment_policy.filters]\n"
            'Path = "include"\nCUSTOM = "include"\nPrivate = "exclude"\n',
        ):
            with self.subTest(original=original):
                self.config.write_text(
                    original + "[shell_environment_policy.set]\n"
                    'CUSTOM = "synthetic-value"\n'
                )
                self.reconcile()
                policy = tomllib.loads(self.config.read_text())[
                    "shell_environment_policy"
                ]
                self.assertEqual(
                    policy["filters"],
                    {
                        "path": "include",
                        "custom": "include",
                        "private": "exclude",
                        "ssh_auth_sock": "exclude",
                    },
                )
                self.assertEqual(policy["set"]["CUSTOM"], "synthetic-value")

    def test_unsupported_legacy_constraints_are_not_silently_removed(self):
        for original in (
            "[sandbox_workspace_write]\nexclude_slash_tmp = true\n",
            "[sandbox_workspace_write]\nexclude_tmpdir_env_var = true\n",
            '[shell_environment_policy]\nfilters = { PATH = "include" }\n'
            'exclude = ["PRIVATE"]\n',
        ):
            with self.subTest(original=original):
                self.config.write_text(original)
                self.reconcile(success=False)
                self.assertEqual(self.config.read_text(), original)
                self.assertFalse((self.state / "pending-roots").exists())

    def test_malformed_input_preserves_config_and_ownership(self):
        for content, pending in (
            ('model = "unterminated\n', ""),
            ('[shell_environment_policy]\nset = "synthetic-value"\n', ""),
            ('model = "operator-model"\n', '["relative-root"]'),
            ('model = "operator-model"\n', '{"bad": "intent"}'),
        ):
            with self.subTest(content=content, pending=pending):
                self.config.write_text(content)
                (self.state / "plans-root").write_text(
                    self.roots["plans"] + "\n"
                )
                (self.state / "pending-roots").write_text(pending)
                result = self.reconcile(success=False)
                self.assertNotIn("synthetic-value", result.stderr)
                self.assertEqual(self.config.read_text(), content)
                self.assertEqual(
                    (self.state / "pending-roots").read_text(), pending
                )
                self.assertEqual(
                    (self.state / "plans-root").read_text(),
                    self.roots["plans"] + "\n",
                )

    def test_existing_profile_restrictions_are_not_discarded(self):
        for original in (
            '[permissions.managed]\nextends = ":read-only"\n',
            '[permissions.managed]\nextends = "operator-policy"\n',
            '[permissions.managed.filesystem]\n"/operator/private" = "deny"\n',
            "[permissions.managed.filesystem]\n"
            '"/operator/readonly" = "read"\n',
            '[permissions.managed.filesystem.":workspace_roots"]\n'
            '"docs" = "read"\n',
        ):
            with self.subTest(original=original):
                self.config.write_text(original)
                (self.state / "plans-root").write_text(
                    self.roots["plans"] + "\n"
                )
                (self.state / "pending-roots").write_text(
                    json.dumps(list(self.roots.values()))
                )
                before = {
                    p.name: p.read_bytes()
                    for p in self.state.iterdir()
                    if p.name != "reconcile.lock"
                }
                result = self.reconcile(success=False)
                self.assertIn("restriction", result.stderr)
                self.assertEqual(self.config.read_text(), original)
                self.assertEqual(
                    {
                        p.name: p.read_bytes()
                        for p in self.state.iterdir()
                        if p.name != "reconcile.lock"
                    },
                    before,
                )

    def test_managed_denials_preserve_or_tighten_matching_restrictions(self):
        for access in ("read", "deny"):
            with self.subTest(access=access):
                self.config.write_text(
                    '[permissions.managed]\nextends = ":workspace"\n'
                    "[permissions.managed.filesystem]\n"
                    f"{json.dumps(str(self.home / 'protected'))}"
                    f' = "{access}"\n'
                    '"/broad" = "write"\n'
                )
                self.reconcile()
                profile = tomllib.loads(self.config.read_text())[
                    "permissions"
                ]["managed"]
                self.assertEqual(
                    profile["filesystem"],
                    {str(self.home / "protected"): "deny"},
                )


if __name__ == "__main__":
    unittest.main()
