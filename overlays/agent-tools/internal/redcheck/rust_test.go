package redcheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustSuffixBoundary(t *testing.T) {
	rustOptions(t)
	for _, atom := range []string{"1r", "1br", "1cr", "1.0r", "1e+2r", "0x1r", "0b1r", `"x"r`, `r"x"r`, `'x'r`, `b'x'r`, `b"x"r`, `br"x"r`, `c"x"r`, `cr"x"r`, "r#r", "'r#r"} {
		t.Run(atom, func(t *testing.T) {
			source := fmt.Sprintf(`macro_rules! m { ($($t:tt)*) => {} }
m!{ %s"\" }
#[cfg(test)] mod fake {
" }
pub mod wrap { pub fn prod() -> i32 { 2 } // "
#[cfg(test)] mod tests { #[test] fn checks() { assert_eq!(super::prod(), 2); } }
}

`, atom)
			dir := t.TempDir()
			write(t, dir, "session.rs", source)
			binary := filepath.Join(dir, "checks")
			cmd := exec.CommandContext(t.Context(), "rustc", "--edition=2021", "--test", filepath.Join(dir, "session.rs"), "-o", binary)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compiler rejected reproduction: %v\n%s", err, out)
			}
			if out, err := exec.CommandContext(t.Context(), binary).CombinedOutput(); err != nil {
				t.Fatalf("compiled reproduction failed: %v\n%s", err, out)
			}
			module, err := RustTestContent([]byte(source))
			if err == nil {
				t.Fatalf("accepted candidate inside a string, copying production into tests: %s", module)
			}
			changed := strings.ReplaceAll(source, "{ 2 }", "{ 3 }")
			if projected, _, _, err := projectRust([]byte(source), []byte(changed)); err == nil || projected != nil {
				t.Fatalf("unsafe projection returned bytes: %q, %v", projected, err)
			}
		})
	}
}

const rustBug = "fn add(a: i32, b: i32) -> i32 { a - b }\n"
const rustFix = "fn add(a: i32, b: i32) -> i32 { a + b }\n"
const rustTests = "#[cfg(test)]\nmod test { #[test] fn adds() { assert_eq!(super::add(2, 3), 5); } }\n"

func rustOptions(t *testing.T) Options {
	t.Helper()
	compiler, err := exec.LookPath("rustc")
	if err != nil {
		t.Skip("real Rust tests require rustc on PATH; the Nix package supplies it")
	}
	build := "'" + strings.ReplaceAll(compiler, "'", "'\\''") + "' --test session.rs -o checks"
	return Options{Test: build + " && ./checks", Build: build, TestFiles: []string{}, RustTestFiles: []string{"*.rs"}}
}

func TestRustVerdict(t *testing.T) {
	for _, tc := range []struct{ name, before, after, verdict string }{
		{"added module", rustBug, rustFix + rustTests, FailsAsAsserted},
		{"modified module", rustBug + strings.Replace(rustTests, "5)", "-1)", 1), rustFix + rustTests, FailsAsAsserted},
		{"unrelated test", rustBug, rustFix + strings.Replace(rustTests, "super::add(2, 3)", "5", 1), PassesWithoutChange},
		{"new API", rustBug, rustFix + "fn answer() -> i32 { 5 }\n" + strings.Replace(rustTests, "super::add(2, 3)", "super::answer()", 1), FailsToBuildWithoutChange},
		{"fails with change", rustBug, rustFix + strings.Replace(rustTests, "5)", "8)", 1), FailsWithChange},
		{"tests only", rustBug + "\n", rustBug + rustTests, NoCodeChange},
		{"tests only with separators", rustFix, rustFix + "\n" + rustTests, NoCodeChange},
		{"tests only with trailing comments", rustFix + "// old\n", rustFix + "// new\n" + rustTests + "// suffix\n", NoCodeChange},
		{"internal whitespace remains production", rustFix, strings.Replace(rustFix, "a + b", "a  + b", 1) + rustTests, PassesWithoutChange},
		{"production only", rustBug + rustTests, rustFix + rustTests, NoTestFiles},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := rustOptions(t)
			dir := newRepo(t)
			write(t, dir, "session.rs", tc.before)
			commit(t, dir, "Add Rust calculation")
			write(t, dir, "session.rs", tc.after)
			commit(t, dir, "Change Rust calculation")
			r := check(t, dir, "HEAD", options)
			c := only(t, r)
			if c.Verdict != tc.verdict {
				t.Fatalf("verdict %s; want %s: %+v", c.Verdict, tc.verdict, c)
			}
			if tc.verdict == FailsAsAsserted && (c.Build.Exit != 0 || c.Without.Exit == 0 || c.With.Exit != 0 || !strings.Contains(c.Without.Output, "assertion")) {
				t.Fatalf("expected compiled failing assertion then passing test: %+v", c)
			}
		})
	}
}

func TestRustProjection(t *testing.T) {
	prefix := "// café #[cfg(test)] mod fake {}\n/* nested /* } */ } #[cfg(test)] mod fake {} */\n" +
		"const UNICODE: &str = \"é🦀\"; const CHAR: char = 'é';\n" +
		"const S: &str = r###\"a\" } #[cfg(test)] mod fake {} \"b\"###;\n" +
		"const B: &[u8] = br#\"} #\"#;\nconst C: char = '\\u{7d}';\n" +
		"const Q: &str = \"\\\" }\"; const BC: u8 = b'}';\n" +
		"fn borrow<'a>(v: &'a str) -> &'a str { v }\n" + rustBug
	old := prefix + "#[cfg(test)] mod test {}\n// old suffix\n"
	new := strings.Replace(prefix, "a - b", "a + b", 1) + rustTests + "// new suffix\n"
	projected, tests, code, err := projectRust([]byte(old), []byte(new))
	if err != nil || !tests || !code || string(projected) != prefix+strings.TrimSuffix(rustTests, "\n")+"\n// old suffix\n" {
		t.Fatalf("projection %q, tests=%v code=%v err=%v", projected, tests, code, err)
	}
}

func TestRustRefusal(t *testing.T) {
	t.Run("parent line comment without newline", func(t *testing.T) {
		if _, _, _, err := projectRust([]byte(rustBug+"// note"), []byte(rustFix+"\n"+rustTests)); err == nil {
			t.Fatal("appended the test gate to the parent's line comment")
		}
	})
	for name, source := range map[string]string{
		"nested":                 "mod outer { " + rustTests + " }",
		"macro candidate":        "macro_rules! fake { () => { " + rustTests + " } }",
		"multiple":               rustTests + rustTests,
		"followed by production": rustTests + rustFix,
		"out of line":            "#[cfg(test)] mod test;",
		"extra attribute":        "#[allow(dead_code)]\n" + rustTests,
		"doc attribute":          "/// Test module docs\n" + rustTests,
		"trailing doc attribute": rustTests + "/// dangling\n",
		"Unicode identifier":     "fn café() {}\n" + rustTests,
		"Unicode whitespace":     "fn value() {}\u0085" + rustTests,
		"BOM":                    "\ufeff" + rustTests,
		"shebang":                "#!/usr/bin/rust-script\n" + rustTests,
		"control":                "\x00" + rustTests,
		"cfg attr":               "#[cfg_attr(test, allow(dead_code))]\n" + rustTests,
		"attribute after cfg":    "#[cfg(test)] #[allow(dead_code)] mod test {}",
		"raw module name":        "#[cfg(test)] mod r#test {}",
		"unclosed comment":       "/* bad " + rustTests,
		"unclosed string":        "const S: &str = \"bad " + rustTests,
		"unclosed raw string":    "const S: &str = r#\"bad " + rustTests,
		"unbalanced":             "fn bad() {]\n" + rustTests,
		"character":              "const C: char = '\\q;\n" + rustTests,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := projectRust([]byte(rustBug), []byte(source)); err == nil {
				t.Fatal("accepted an unsupported module boundary")
			}
		})
	}
}

func TestRustSupport(t *testing.T) {
	options := rustOptions(t)
	options.RustTestFiles = []string{"session.rs"}
	dir := newRepo(t)
	before := "use std::cmp::min as choose;\nfn value() -> i32 { choose(2, 3) }\n"
	write(t, dir, "session.rs", before+"#[cfg(test)] mod test { #[test] fn old() { assert_eq!(super::value(), 2); } }\n")
	write(t, dir, "test-support.txt", "// test helpers\n")
	commit(t, dir, "Add selection and tests")
	write(t, dir, "session.rs", strings.Replace(before, "::min", "::max", 1)+"#[cfg(test)] mod test { include!(\"test-support.txt\"); #[test] fn selects() { assert_eq!(super::value(), expected()); } }\n")
	write(t, dir, "test-support.txt", "fn expected() -> i32 { 3 }\n")
	commit(t, dir, "Fix selection with new test helper")
	if result := check(t, dir, "HEAD", options); only(t, result).Verdict != FailsToBuildWithoutChange {
		t.Fatalf("unselected support should give build-only evidence: %+v", only(t, result))
	}
	options.TestFiles = []string{"test-support.txt"}
	if result := check(t, dir, "HEAD", options); only(t, result).Verdict != FailsAsAsserted {
		t.Fatalf("explicit support should compile and expose assertion: %+v", only(t, result))
	}
}

func TestRustSelectionRefusal(t *testing.T) {
	for _, kind := range []string{"overlap", "new", "deleted", "mode", "non-Rust"} {
		t.Run(kind, func(t *testing.T) {
			dir := newRepo(t)
			path := "session.rs"
			if kind == "non-Rust" {
				path = "session.txt"
			}
			if kind != "new" {
				write(t, dir, path, rustBug)
				commit(t, dir, "Add source")
			}
			write(t, dir, path, rustFix+rustTests)
			if kind == "deleted" {
				gitIn(t, dir, "update-index", "--force-remove", path)
			}
			if kind == "mode" {
				if err := os.Chmod(filepath.Join(dir, path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "deleted" {
				gitIn(t, dir, "commit", "-q", "-m", "Delete source from tree")
			} else {
				commit(t, dir, "Change source")
			}
			opts := Options{Dir: dir, Test: "true", RustTestFiles: []string{path}}
			if kind == "overlap" {
				opts.TestFiles = []string{path}
			}
			defer untouched(t, dir)()
			if _, err := Do(t.Context(), "HEAD", opts); err == nil {
				t.Fatal("accepted an unsafe source selection")
			}
		})
	}
}
