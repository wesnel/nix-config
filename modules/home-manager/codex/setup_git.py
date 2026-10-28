from collections.abc import Mapping
import json
import os
from pathlib import Path
import secrets
import stat
import subprocess
import sys

import tomlkit

from permission_paths import validate_grants

PROFILE_NAMES = json.loads(
    Path(__file__).with_name("profiles.json").read_text()
)
BASE_PROFILE = PROFILE_NAMES["base"]
PROJECT_PROFILE = PROFILE_NAMES["project"]


def permission_path(path):
    value = str(path)
    if any(character in value for character in "\n\r\x00*?[]{}\\"):
        raise ValueError(
            "permission paths must contain no control or glob characters"
        )
    return value


def git_environment():
    environment = {
        key: value for key, value in os.environ.items()
        if not key.startswith("GIT_")
    }
    environment.update(GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1")
    return environment


def git_path(repository, argument, environment):
    result = subprocess.run(
        ["git", "-c", "protocol.allow=never", "-C", str(repository),
         "rev-parse", "--path-format=absolute", argument],
        env=environment, text=True, capture_output=True,
    )
    if result.returncode:
        raise ValueError("target must be an existing non-bare Git worktree")
    value = result.stdout.removesuffix("\n")
    if not value or any(character in value for character in "\n\r\x00"):
        raise ValueError("Git paths must contain no newline or NUL")
    path = Path(value)
    if not path.is_absolute():
        raise ValueError("Git did not return an absolute metadata path")
    resolved = path.resolve(strict=True)
    permission_path(resolved)
    return resolved


def base_profile():
    directory = Path(os.environ.get("CODEX_HOME", Path.home() / ".codex"))
    try:
        settings = tomlkit.parse((directory / "config.toml").read_text())
    except FileNotFoundError:
        raise ValueError(
            "activate the managed wgn-workspace profile first"
        ) from None
    permissions = settings.get("permissions", {})
    profile = (
        permissions.get(BASE_PROFILE, {})
        if isinstance(permissions, Mapping) else {}
    )
    if (settings.get("default_permissions") != BASE_PROFILE
            or not isinstance(profile, Mapping)
            or profile.get("extends") != ":workspace"):
        raise ValueError("activate the managed wgn-workspace profile first")
    if ("sandbox_mode" in settings or "sandbox_workspace_write" in settings
            or PROJECT_PROFILE in permissions):
        raise ValueError(
            "user configuration needs explicit permission-profile migration"
        )
    return profile.get("filesystem", {})


def filesystem_rules(gitdir, common):
    rules = {str(common): "write", str(gitdir): "write"}
    for directory, names in (
        (common, (
            "config", "config.worktree", "hooks", "info", "modules",
            "worktrees",
        )),
        (gitdir, ("config", "config.worktree", "hooks", "info")),
    ):
        for name in names:
            path = directory / name
            if path.is_symlink():
                raise ValueError(
                    "protected Git metadata symlinks are unsupported"
                )
            rules[str(path)] = "read"
    return rules


def validate_migration(document, permissions, desired):
    selected = document.get("default_permissions", BASE_PROFILE)
    if (selected not in (BASE_PROFILE, PROJECT_PROFILE)
            or (selected == PROJECT_PROFILE
                and PROJECT_PROFILE not in permissions)):
        raise ValueError("selected project profile needs explicit migration")
    if document.get("sandbox_mode", "workspace-write") != "workspace-write":
        raise ValueError("legacy sandbox mode needs explicit migration")
    if "network_proxy" in document.get("features", {}):
        raise ValueError("project network override needs explicit migration")
    legacy = document.get("sandbox_workspace_write", {})
    if not isinstance(legacy, Mapping) or set(legacy) - {
        "writable_roots", "exclude_tmpdir_env_var", "exclude_slash_tmp",
    }:
        raise ValueError("unsupported legacy sandbox constraints")
    for name in ("exclude_tmpdir_env_var", "exclude_slash_tmp"):
        if legacy.get(name, False) is not False:
            raise ValueError("legacy temporary restrictions need migration")
    roots = legacy.get("writable_roots", [])
    if (not isinstance(roots, list)
            or any(not isinstance(p, str) for p in roots)):
        raise ValueError("invalid legacy writable roots")
    if PROJECT_PROFILE not in permissions:
        return
    previous = permissions[PROJECT_PROFILE]
    if (not isinstance(previous, Mapping)
            or previous.get("extends") != BASE_PROFILE
            or set(previous) - {"extends", "filesystem"}):
        raise ValueError("project profile constraints need explicit migration")
    filesystem = previous.get("filesystem", {})
    if not isinstance(filesystem, Mapping):
        raise ValueError("project filesystem rules must be a table")
    for path, access in filesystem.items():
        if access != "write" and desired["filesystem"].get(path) != access:
            raise ValueError(
                "project filesystem restrictions need explicit migration"
            )


def setup(repository):
    supplied = str(repository)
    if any(character in supplied for character in "\n\r\x00"):
        raise ValueError("repository paths must contain no newline or NUL")
    repository = Path(repository).resolve(strict=True)
    permission_path(repository)
    environment = git_environment()
    if git_path(repository, "--show-toplevel", environment) != repository:
        raise ValueError("target must be the Git worktree's repository root")
    gitdir = git_path(repository, "--git-dir", environment)
    common = git_path(repository, "--git-common-dir", environment)
    if not gitdir.is_dir() or not common.is_dir():
        raise ValueError("Git metadata paths must be directories")
    inherited_filesystem = base_profile()
    profile = {
        "extends": BASE_PROFILE,
        "filesystem": filesystem_rules(gitdir, common),
    }
    validate_grants(profile["filesystem"], inherited_filesystem)
    directory = repository / ".codex"
    path = directory / "config.toml"
    if directory.is_symlink() or path.is_symlink():
        raise ValueError(
            "project .codex directory and config must not be symlinks"
        )
    if directory.exists() and not directory.is_dir():
        raise ValueError("project .codex path must be a directory")
    if path.exists() and not path.is_file():
        raise ValueError("project Codex config must be a regular file")
    directory.mkdir(mode=0o700, exist_ok=True)
    directory_descriptor = os.open(
        directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    )
    try:
        try:
            descriptor = os.open(
                "config.toml", os.O_RDONLY | os.O_NOFOLLOW,
                dir_fd=directory_descriptor,
            )
        except FileNotFoundError:
            original = ""
        else:
            with os.fdopen(descriptor) as stream:
                if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                    raise ValueError(
                        "project Codex config must be a regular file"
                    )
                original = stream.read()
        document = tomlkit.parse(original)
        permissions = document.get("permissions", {})
        if not isinstance(permissions, Mapping):
            raise ValueError("project permissions must be a table")
        if BASE_PROFILE in permissions:
            raise ValueError(
                "project overrides the managed base profile wgn-workspace"
            )
        features = document.get("features", {})
        if not isinstance(features, Mapping):
            raise ValueError("project features must be a table")
        validate_migration(document, permissions, profile)
        document.pop("sandbox_mode", None)
        document.pop("sandbox_workspace_write", None)
        document["default_permissions"] = PROJECT_PROFILE
        profiles = document.setdefault("permissions", tomlkit.table())
        profiles[PROJECT_PROFILE] = profile
        text = tomlkit.dumps(document)
        if text == original:
            return path
        temporary = ".git-profile-" + secrets.token_hex(12) + ".tmp"
        descriptor = os.open(
            temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
            0o600, dir_fd=directory_descriptor,
        )
        try:
            with os.fdopen(descriptor, "w") as stream:
                stream.write(text)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(
                temporary, "config.toml", src_dir_fd=directory_descriptor,
                dst_dir_fd=directory_descriptor,
            )
        finally:
            try:
                os.unlink(temporary, dir_fd=directory_descriptor)
            except FileNotFoundError:
                pass
    finally:
        os.close(directory_descriptor)
    return path


def main():
    if len(sys.argv) != 2:
        print("usage: codex-git-setup REPOSITORY", file=sys.stderr)
        return 2
    try:
        path = setup(sys.argv[1])
    except (OSError, ValueError, tomlkit.exceptions.ParseError) as error:
        print("codex-git-setup: " + str(error), file=sys.stderr)
        return 2
    print("Configured project Git permissions in " + str(path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
