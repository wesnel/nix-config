"""Check the lint policy with the golangci-lint executable on PATH."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


CONFIG = Path(sys.argv.pop(1)).resolve()


def cognitive(branches):
    body = "".join(f"if x == {i} {{ x++ }}\n" for i in range(branches))
    return f"package fixture\nfunc Cognitive(x int) int {{\n{body}return x\n}}\n"


def methods(count):
    body = "".join(f"Method{i}()\n" for i in range(count))
    return f"package fixture\ntype Service interface {{\n{body}}}\n"


class LintPolicy(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.linter = shutil.which("golangci-lint")
        if cls.linter is None:
            raise RuntimeError("golangci-lint must be on PATH")
        cls.scratch = tempfile.TemporaryDirectory(prefix="agent-tools-lint-")
        cls.addClassCleanup(cls.scratch.cleanup)
        cls.root = Path(cls.scratch.name)
        cls.env = dict(os.environ, HOME=str(cls.root / "home"),
                       GOCACHE=str(cls.root / "go-cache"),
                       GOMODCACHE=str(cls.root / "go-mod"),
                       GOLANGCI_LINT_CACHE=str(cls.root / "lint-cache"),
                       GOPROXY="off", GOSUMDB="off", GOWORK="off",
                       GOFLAGS="-mod=mod", GOTOOLCHAIN="local")
        Path(cls.env["HOME"]).mkdir()

    def check(self, source, expected, filename="fixture.go"):
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            module = Path(directory)
            (module / "go.mod").write_text("module example.com/fixture\n\ngo 1.25\n")
            (module / "base.go").write_text("package fixture\n")
            (module / filename).write_text(source)
            report = module / "issues.json"
            result = subprocess.run(
                [self.linter, "run", "--config", str(CONFIG),
                 "--output.json.path", str(report), "./..."],
                cwd=module, env=self.env, capture_output=True, text=True, timeout=120)
            detail = result.stdout + result.stderr
            self.assertEqual(result.returncode, 1 if expected else 0, detail)
            self.assertTrue(report.exists(), detail)
            issues = json.loads(report.read_text())["Issues"] or []
            self.assertEqual({issue["FromLinter"] for issue in issues}, expected, detail)
            return issues

    def test_cognitive_limit(self):
        for count, expected in [(20, set()), (21, {"gocognit"})]:
            with self.subTest(complexity=count):
                issues = self.check(cognitive(count), expected)
                if expected:
                    self.assertIn("complexity 21", issues[0]["Text"])

    def test_cognitive_tests_are_exempt(self):
        self.check(cognitive(21), set(), "fixture_test.go")

    def test_interface_limit(self):
        for count, expected in [(10, set()), (11, {"interfacebloat"})]:
            with self.subTest(methods=count):
                self.check(methods(count), expected)

    def test_test_interfaces_are_checked(self):
        self.check(methods(11), {"interfacebloat"}, "fixture_test.go")

    def test_security_is_checked(self):
        self.check('package fixture\nimport "os"\n'
                   'func Write() error { return os.WriteFile("public", nil, 0777) }\n',
                   {"gosec"})

    def test_security_test_exemption_is_preserved(self):
        self.check('package fixture\nimport "os"\n'
                   'func Write() error { return os.WriteFile("public", nil, 0777) }\n',
                   set(), "fixture_test.go")

    def test_suppression_needs_a_reason(self):
        self.check('package fixture\nimport "os"\n'
                   'func Write() error {\n'
                   '//nolint:gosec\nreturn os.WriteFile("public", nil, 0777)\n}\n',
                   {"nolintlint"})

    def test_suppression_needs_a_linter(self):
        self.check('package fixture\nimport "os"\n'
                   'func Write() error {\n'
                   '//nolint // Fixture for a missing linter name.\n'
                   'return os.WriteFile("public", nil, 0777)\n}\n',
                   {"nolintlint"})


if __name__ == "__main__":
    unittest.main()
