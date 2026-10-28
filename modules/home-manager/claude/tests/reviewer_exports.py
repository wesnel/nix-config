import json
from pathlib import Path
import re
import sys


def check(agents, source, skills, guard):
    errors = []
    reviewer = agents.get("adversarial-reviewer")
    if not isinstance(reviewer, dict):
        return ["headless agents omit adversarial-reviewer"]

    frontmatter = source.split("\n---\n", 1)[0]
    preloads = re.search(r"^skills:\n((?:  - [^\n]+(?:\n|$))+)", frontmatter, re.MULTILINE)
    if preloads is None:
        return ["adversarial-reviewer has no supported skills list"]
    for line in preloads[1].splitlines():
        name = line.removeprefix("  - ")
        if name not in skills:
            errors.append(f"adversarial-reviewer preload {name} is not configured")
            continue
        text = Path(skills[name]).read_text()
        _, separator, body = text.partition("\n---\n")
        if not separator or not body.strip():
            errors.append(f"adversarial-reviewer preload {name} has no body")
        elif body not in reviewer.get("prompt", ""):
            errors.append(f"adversarial-reviewer prompt omits preload {name}")

    hook = {"type": "command", "command": f"{guard} guard --reviewer", "onFailure": "block"}
    if not any(
        group.get("matcher") == "*" and hook in group.get("hooks", [])
        for group in reviewer.get("hooks", {}).get("PreToolUse", [])
    ):
        errors.append("adversarial-reviewer lacks its blocking reviewer hook for every tool")
    return errors


if __name__ == "__main__":
    agents, source, skills, guard = sys.argv[1:]
    errors = check(
        json.loads(Path(agents).read_text()),
        Path(source).read_text(),
        json.loads(Path(skills).read_text()),
        guard,
    )
    for error in errors:
        print(error, file=sys.stderr)
    sys.exit(bool(errors))
