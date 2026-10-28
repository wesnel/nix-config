from collections.abc import Mapping
from fnmatch import fnmatchcase
from pathlib import Path


class PermissionPolicyError(ValueError):
    pass


def mapping(value, label):
    if not isinstance(value, Mapping):
        raise PermissionPolicyError(label + " must be a table")
    return value


def absolute(path):
    if not isinstance(path, str) or not path or "\n" in path or "\0" in path:
        raise PermissionPolicyError("writable root must be an absolute path")
    expanded = Path(path).expanduser()
    if not expanded.is_absolute():
        raise PermissionPolicyError("writable root must be an absolute path")
    return expanded.resolve()


def matches(path, pattern):
    return fnmatchcase(path, pattern) or fnmatchcase(
        path, pattern.replace("**/", "")
    )


def validate_grants(grants, filesystem, label="filesystem grant"):
    for root, granted in mapping(grants, "filesystem grants").items():
        resolved = absolute(root)
        if granted not in ("read", "write"):
            raise PermissionPolicyError("unsupported filesystem grant")
        parents = [resolved, *resolved.parents]
        for base, rule in mapping(
            filesystem, "filesystem restrictions"
        ).items():
            rules = rule if isinstance(rule, Mapping) else {".": rule}
            for child, access in rules.items():
                if access not in ("read", "write", "deny"):
                    raise PermissionPolicyError(
                        "unsupported filesystem restriction"
                    )
                if access != "write" and any(
                    char in base or char in child for char in "{}[]\\"
                ):
                    raise PermissionPolicyError(
                        "unsupported filesystem pattern syntax"
                    )
                if access == "write" or (
                    access == "read" and granted == "read"
                ):
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
                    raise PermissionPolicyError(
                        "unsupported managed restriction scope"
                    )
                else:
                    denied = absolute(
                        base if child == "." else base + "/" + child
                    )
                    conflict = any(
                        matches(str(parent), str(denied)) for parent in parents
                    )
                if conflict:
                    raise PermissionPolicyError(
                        label
                        + " conflicts with explicit filesystem restriction: "
                        + root
                    )


def validate_roots(roots, filesystem):
    grants = {}
    for root, enabled in mapping(roots, "workspace roots").items():
        absolute(root)
        if type(enabled) is not bool:
            raise PermissionPolicyError(
                "workspace root entries must be booleans"
            )
        if enabled:
            grants[root] = "write"
    validate_grants(grants, filesystem, "writable root")
