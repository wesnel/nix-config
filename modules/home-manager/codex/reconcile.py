from collections.abc import Mapping
import copy
import fcntl
from fnmatch import fnmatchcase
import json
import os
from pathlib import Path
import sys

import tomlkit


class ReconcileError(Exception):
    pass


def mapping(value, label):
    if not isinstance(value, Mapping):
        raise ReconcileError(label + " must be a table")
    return value


def absolute(path):
    if not isinstance(path, str) or not path or "\n" in path or "\0" in path:
        raise ReconcileError("writable root must be an absolute path")
    expanded = Path(path).expanduser()
    if not expanded.is_absolute():
        raise ReconcileError("writable root must be an absolute path")
    return expanded.resolve()


def matches(path, pattern):
    return fnmatchcase(path, pattern) or fnmatchcase(
        path, pattern.replace("**/", "")
    )


def validate_roots(roots, filesystem):
    for root, enabled in mapping(roots, "workspace roots").items():
        resolved = absolute(root)
        if type(enabled) is not bool:
            raise ReconcileError("workspace root entries must be booleans")
        if not enabled:
            continue
        parents = [resolved, *resolved.parents]
        for base, rule in filesystem.items():
            rules = rule if isinstance(rule, Mapping) else {".": rule}
            for child, access in rules.items():
                if access != "deny":
                    continue
                if base == ":workspace_roots":
                    candidates = [
                        str(parent).lstrip("/") for parent in parents
                    ]
                    conflict = any(
                        matches(candidate, child) for candidate in candidates
                    )
                elif base == ":root":
                    conflict = True
                elif base.startswith(":"):
                    raise ReconcileError("unsupported managed denial scope")
                else:
                    denied = absolute(
                        base if child == "." else base + "/" + child
                    )
                    conflict = any(
                        matches(str(parent), str(denied)) for parent in parents
                    )
                if conflict:
                    raise ReconcileError(
                        "writable root conflicts with managed read denial: "
                        + root
                    )


def environment(document, desired):
    existing = document.setdefault("shell_environment_policy", tomlkit.table())
    mapping(existing, "shell environment policy")
    legacy = any(key in existing for key in ("exclude", "include_only"))
    if legacy and "filters" in existing:
        raise ReconcileError(
            "shell environment mixes legacy and canonical filters"
        )
    filters = dict(mapping(existing.get("filters", {}), "environment filters"))
    for key, access in (("include_only", "include"), ("exclude", "exclude")):
        patterns = existing.pop(key, [])
        if not isinstance(patterns, list) or any(
            not isinstance(p, str) for p in patterns
        ):
            raise ReconcileError(
                "legacy environment filters must be string arrays"
            )
        filters.update({pattern: access for pattern in patterns})
    filters.update(desired.get("filters", {}))
    if any(
        access not in ("include", "exclude") for access in filters.values()
    ):
        raise ReconcileError("invalid environment filter action")
    denied = [
        pattern.upper()
        for pattern, access in filters.items()
        if access == "exclude"
    ]
    if not desired.get(
        "ignore_default_excludes",
        existing.get("ignore_default_excludes", True),
    ):
        denied += ["*KEY*", "*SECRET*", "*TOKEN*"]
    values = dict(mapping(existing.get("set", {}), "explicit environment"))
    values.update(desired.get("set", {}))
    for name, value in values.items():
        if not isinstance(value, str):
            raise ReconcileError("explicit environment values must be strings")
        if any(fnmatchcase(name.upper(), pattern) for pattern in denied):
            raise ReconcileError(
                "explicit environment value conflicts with exclusion: " + name
            )
    desired["filters"] = filters


def atomic_write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    staged = path.with_name(path.name + ".next")
    descriptor = os.open(
        staged, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600
    )
    with os.fdopen(descriptor, "w") as output:
        output.write(text)
        output.flush()
        os.fsync(output.fileno())
    os.replace(staged, path)
    descriptor = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def read_text(path):
    return path.read_text() if path.exists() else ""


def merge(table, values, owned, path=()):
    for key, value in values.items():
        location = path + (key,)
        if isinstance(value, dict) and location not in owned:
            child = table.setdefault(key, tomlkit.table())
            merge(child, value, owned, location)
        elif table.get(key) != value:
            table[key] = value


def reconcile(config, settings, roots, state):
    if set(roots) != {"plans", "cache"}:
        raise ReconcileError("managed roots must name plans and cache")
    for root in roots.values():
        absolute(root)
    state.mkdir(parents=True, exist_ok=True)
    with (state / "reconcile.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        original = read_text(config)
        document = tomlkit.parse(original)
        name = settings["default_permissions"]
        profile = document.get("permissions", {}).get(name, {})
        saved_roots = dict(profile.get("workspace_roots", {}))
        legacy = mapping(
            document.get("sandbox_workspace_write", {}), "legacy sandbox"
        )
        if any(
            legacy.get(key)
            for key in ("exclude_slash_tmp", "exclude_tmpdir_env_var")
        ):
            raise ReconcileError(
                "legacy temporary-directory restrictions need explicit "
                "profile migration"
            )
        if set(legacy) - {
            "writable_roots",
            "network_access",
            "exclude_slash_tmp",
            "exclude_tmpdir_env_var",
        }:
            raise ReconcileError("unknown legacy sandbox setting")
        legacy_roots = legacy.get("writable_roots", [])
        if not isinstance(legacy_roots, list):
            raise ReconcileError("legacy writable roots must be an array")
        for root in legacy_roots:
            absolute(root)
            saved_roots[root] = True
        previous = {
            read_text(state / (key + "-root")).strip() for key in roots
        } - {""}
        pending = state / "pending-roots"
        pending_roots = json.loads(read_text(pending) or "[]")
        if not isinstance(pending_roots, list) or len(pending_roots) > 2 * len(
            roots
        ):
            raise ReconcileError("invalid pending root intent")
        previous.update(pending_roots)
        for root in previous:
            absolute(root)
        intent = sorted((previous & saved_roots.keys()) | set(roots.values()))
        for root in previous:
            saved_roots.pop(root, None)
        saved_roots.update({root: True for root in roots.values()})
        settings = copy.deepcopy(settings)
        settings["permissions"][name]["workspace_roots"] = saved_roots
        validate_roots(
            saved_roots, settings["permissions"][name]["filesystem"]
        )
        environment(document, settings["shell_environment_policy"])

        document.pop("sandbox_mode", None)
        document.pop("sandbox_workspace_write", None)
        owned = {("permissions", name), ("features", "network_proxy")}
        owned.update(
            ("mcp_servers", server)
            for server in settings.get("mcp_servers", {})
        )
        merge(document, settings, owned)
        rendered = tomlkit.dumps(document)
        if (
            rendered == original
            and not read_text(pending)
            and all(
                read_text(state / (key + "-root")) == root + "\n"
                for key, root in roots.items()
            )
        ):
            return
        # Keep only owned roots still present in this atomic config generation,
        # plus its replacement. A crash followed by a new target stays bounded.
        atomic_write(pending, json.dumps(intent) + "\n")
        if rendered != original:
            atomic_write(config, rendered)
        for key, root in roots.items():
            path = state / (key + "-root")
            if read_text(path) != root + "\n":
                atomic_write(path, root + "\n")
        atomic_write(pending, "")


def main():
    try:
        config, settings, roots, state = sys.argv[1:]
        reconcile(
            Path(config), json.loads(settings), json.loads(roots), Path(state)
        )
    except ReconcileError as error:
        sys.exit("codex configuration: " + str(error))
    except (
        OSError,
        ValueError,
        TypeError,
        KeyError,
        tomlkit.exceptions.TOMLKitError,
    ):
        sys.exit(
            "codex configuration: invalid input or configuration "
            "could not be written"
        )


if __name__ == "__main__":
    main()
