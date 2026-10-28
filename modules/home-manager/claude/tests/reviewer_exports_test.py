from copy import deepcopy
from pathlib import Path
import tempfile
import unittest

from reviewer_exports import check


class ReviewerExports(unittest.TestCase):
    def test_exports(self):
        with tempfile.TemporaryDirectory() as directory:
            skill = Path(directory) / "SKILL.md"
            skill.write_text("---\nname: fixture\n---\nRequired fixture body.\n")
            source = "---\nname: adversarial-reviewer\nskills:\n  - fixture\n---\nReview.\n"
            skills = {"fixture": str(skill)}
            hook = {"type": "command", "command": "/fixture/pr-loop guard --reviewer", "onFailure": "block"}
            agents = {"adversarial-reviewer": {
                "prompt": "Review.\nRequired fixture body.\n",
                "hooks": {"PreToolUse": [{"matcher": "*", "hooks": [hook]}]},
            }}
            self.assertEqual(check(agents, source, skills, "/fixture/pr-loop"), [])
            cases = [
                ("missing reviewer", {}, source, "headless agents omit adversarial-reviewer"),
                ("unsupported preload", agents, source.replace("  - fixture", "  - unsupported"), "preload unsupported is not configured"),
            ]
            missing_body = deepcopy(agents)
            missing_body["adversarial-reviewer"]["prompt"] = "Review.\n"
            cases.append(("missing preload body", missing_body, source, "prompt omits preload fixture"))
            for field, value in [("command", "/fixture/pr-loop guard"), ("onFailure", "continue"), ("type", "prompt")]:
                broken = deepcopy(agents)
                broken["adversarial-reviewer"]["hooks"]["PreToolUse"][0]["hooks"][0][field] = value
                cases.append((f"bad hook {field}", broken, source, "blocking reviewer hook"))
            broken = deepcopy(agents)
            broken["adversarial-reviewer"]["hooks"]["PreToolUse"][0]["matcher"] = "Bash"
            cases.append(("partial hook matcher", broken, source, "blocking reviewer hook"))
            for name, candidate, agent_source, expected in cases:
                with self.subTest(name=name):
                    errors = check(candidate, agent_source, skills, "/fixture/pr-loop")
                    self.assertTrue(any(expected in error for error in errors), errors)


if __name__ == "__main__":
    unittest.main()
