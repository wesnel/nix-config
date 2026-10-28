import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import tomllib
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("setup_git.py")


class SetupGitTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.home = self.root / "home"
        self.home.mkdir()
        self.codex = self.home / ".codex"
        self.codex.mkdir()
        (self.codex / "config.toml").write_text(
            'default_permissions = "wgn-workspace"\n'
            '[permissions.wgn-workspace]\nextends = ":workspace"\n'
        )
        self.environment = dict(os.environ)
        for name in list(self.environment):
            if name.startswith("GIT_"):
                self.environment.pop(name)
        self.environment.update(
            HOME=str(self.home), CODEX_HOME=str(self.codex),
            XDG_CONFIG_HOME=str(self.home / ".config"),
            XDG_STATE_HOME=str(self.home / ".local/state"),
            XDG_CACHE_HOME=str(self.home / ".cache"),
            GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1",
            GIT_AUTHOR_NAME="Sandbox Test",
            GIT_AUTHOR_EMAIL="sandbox@example.invalid",
            GIT_COMMITTER_NAME="Sandbox Test",
            GIT_COMMITTER_EMAIL="sandbox@example.invalid",
        )
        self.repository = self.root / "repository"
        self.git(self.root, "init", "-q", "-b", "main", str(self.repository))

    def git(self, repository, *arguments):
        return subprocess.check_output(
            ["git", "-C", str(repository), "-c", "commit.gpgsign=false",
             *arguments],
            env=self.environment, text=True, stderr=subprocess.STDOUT,
        ).rstrip("\n")

    def setup_git(self, repository=None):
        return subprocess.run(
            [sys.executable, str(SCRIPT), str(repository or self.repository)],
            env=self.environment, text=True, capture_output=True,
        )

    def test_normal_repository(self):
        result = self.setup_git()
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.repository / ".codex/config.toml"
        self.assertTrue(path.is_file(), "setup must write a project profile")
        settings = tomllib.loads(path.read_text())
        self.assertEqual(settings["default_permissions"], "wgn-project-git")
        profile = settings["permissions"]["wgn-project-git"]
        self.assertEqual(profile["extends"], "wgn-workspace")
        gitdir = str(self.repository / ".git")
        self.assertEqual(profile["filesystem"], {
            gitdir: "write",
            gitdir + "/config": "read",
            gitdir + "/config.worktree": "read",
            gitdir + "/hooks": "read",
            gitdir + "/info": "read",
            gitdir + "/modules": "read",
            gitdir + "/worktrees": "read",
        })

    def test_linked_worktree(self):
        (self.repository / "tracked").write_text("seed\n")
        self.git(self.repository, "add", "tracked")
        self.git(self.repository, "commit", "-qm", "Fixture seed")
        linked = self.root / "linked"
        self.git(
            self.repository, "worktree", "add", "-qb", "linked", str(linked)
        )
        result = self.setup_git(linked)
        self.assertEqual(result.returncode, 0, result.stderr)
        path = linked / ".codex/config.toml"
        self.assertTrue(
            path.is_file(), "linked worktrees need a project profile"
        )
        permissions = tomllib.loads(path.read_text())["permissions"]
        profile = permissions["wgn-project-git"]
        filesystem = profile["filesystem"]
        common = str(self.repository / ".git")
        gitdir = common + "/worktrees/linked"
        self.assertEqual(filesystem[common], "write")
        self.assertEqual(filesystem[gitdir], "write")
        self.assertEqual(filesystem[common + "/worktrees"], "read")
        self.assertEqual(filesystem[common + "/modules"], "read")
        self.assertEqual(filesystem[common + "/config.worktree"], "read")
        self.assertEqual(filesystem[gitdir + "/config.worktree"], "read")

    def test_preservation_and_repeat(self):
        directory = self.repository / ".codex"
        directory.mkdir()
        path = directory / "config.toml"
        path.write_text(
            '# Operator comment.\nmodel = "operator-model"\n'
            'sandbox_mode = "workspace-write"\n'
            '[sandbox_workspace_write]\nnetwork_access = true\n'
            '[features]\nmemories = false\nnetwork_proxy = false\n'
            '[permissions.other]\nextends = ":read-only"\n'
            '[permissions.wgn-project-git]\nextends = ":workspace"\n'
            '[permissions.wgn-project-git.filesystem]\n"/" = "write"\n'
        )
        result = self.setup_git()
        self.assertEqual(result.returncode, 0, result.stderr)
        text = path.read_text()
        parsed = tomllib.loads(text)
        self.assertEqual(parsed["default_permissions"], "wgn-project-git")
        self.assertEqual(parsed["model"], "operator-model")
        self.assertIn("# Operator comment.", text)
        self.assertEqual(parsed["features"], {"memories": False})
        self.assertEqual(
            parsed["permissions"]["other"]["extends"], ":read-only"
        )
        profile = parsed["permissions"]["wgn-project-git"]
        self.assertNotIn("/", profile["filesystem"])
        self.assertNotIn("sandbox_mode", parsed)
        self.assertNotIn("sandbox_workspace_write", parsed)
        modified = path.stat().st_mtime_ns
        repeated = self.setup_git()
        self.assertEqual(repeated.returncode, 0, repeated.stderr)
        self.assertEqual(path.read_text(), text)
        self.assertEqual(path.stat().st_mtime_ns, modified)

    def test_refusals(self):
        cases = (
            "codex_directory_link", "config_link", "non_root",
            "not_repository",
            "missing_base", "invalid_base", "project_base_override", "newline",
            "glob",
        )
        for case in cases:
            with self.subTest(case=case):
                with tempfile.TemporaryDirectory(dir=self.root) as directory:
                    repository = Path(directory) / "repository"
                    self.git(self.root, "init", "-q", str(repository))
                    codex = repository / ".codex"
                    codex.mkdir()
                    path = codex / "config.toml"
                    path.write_text('model = "preserved"\n')
                    target = repository
                    environment = dict(self.environment)
                    outside = Path(directory) / "outside"
                    if case == "codex_directory_link":
                        codex.rename(outside)
                        codex.symlink_to(outside, target_is_directory=True)
                    elif case == "config_link":
                        path.rename(outside)
                        path.symlink_to(outside)
                    elif case == "non_root":
                        target = repository / "nested"
                        target.mkdir()
                    elif case == "not_repository":
                        target = Path(directory) / "other"
                        target.mkdir()
                    elif case in ("missing_base", "invalid_base"):
                        fake_home = Path(directory) / "codex-home"
                        fake_home.mkdir()
                        if case == "invalid_base":
                            (fake_home / "config.toml").write_text(
                                'default_permissions = ":workspace"\n'
                            )
                        environment["CODEX_HOME"] = str(fake_home)
                    elif case == "project_base_override":
                        path.write_text(
                            '[permissions.wgn-workspace]\n'
                            'extends = ":workspace"\n'
                        )
                    elif case == "newline":
                        target = Path(directory) / "line\nbreak"
                        self.git(self.root, "init", "-q", str(target))
                    elif case == "glob":
                        target = Path(directory) / "glob*path"
                        self.git(self.root, "init", "-q", str(target))
                    before = path.read_text()
                    result = subprocess.run(
                        [sys.executable, str(SCRIPT), str(target)],
                        env=environment, text=True, capture_output=True,
                    )
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertEqual(path.read_text(), before)

    def test_protected_symlink_targets(self):
        configuration = self.repository / "shared-config"
        configuration.write_text("[core]\n")
        git_config = self.repository / ".git/config"
        git_config.rename(self.repository / "original-config")
        git_config.symlink_to(configuration)
        result = self.setup_git()
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.repository / ".codex/config.toml"
        self.assertTrue(
            path.exists(), "setup must protect config symlink targets"
        )
        permissions = tomllib.loads(path.read_text())["permissions"]
        profile = permissions["wgn-project-git"]
        filesystem = profile["filesystem"]
        self.assertEqual(filesystem[str(configuration)], "read")

    def test_usage(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT)], env=self.environment,
            text=True, capture_output=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("usage", result.stderr.lower())

    def test_replace_failure_preserves_original(self):
        directory = self.repository / ".codex"
        directory.mkdir()
        path = directory / "config.toml"
        original = 'model = "preserved"\n'
        path.write_text(original)
        namespace = runpy.run_path(str(SCRIPT))
        with (
            mock.patch.dict(os.environ, self.environment, clear=True),
            mock.patch.object(
                namespace["os"], "replace", side_effect=OSError("injected")
            ),
        ):
            with self.assertRaises(OSError):
                namespace["setup"](self.repository)
        self.assertEqual(path.read_text(), original)
        self.assertEqual(list(directory.iterdir()), [path])


if __name__ == "__main__":
    unittest.main()
