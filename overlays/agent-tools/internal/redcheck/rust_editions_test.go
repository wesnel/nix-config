package redcheck

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustEditions(t *testing.T) {
	rustOptions(t)
	for _, edition := range []string{"2015", "2018", "2021", "2024"} {
		t.Run(edition, func(t *testing.T) {
			for _, atom := range []string{`c"x"`, `cr"x"`, `cr#"x"#`, `cr##"x"##`} {
				t.Run(atom, func(t *testing.T) {
					source := "macro_rules! m { ($($t:tt)*) => {} }\nm! { " + atom + " }\n" + rustFix + rustTests
					compileRustEdition(t, edition, source)
					if _, err := RustTestContent([]byte(source)); err == nil {
						t.Fatal("accepted edition-dependent C string candidate")
					}
				})
			}
			source := "macro_rules! m { ($($t:tt)*) => {} }\nm! { " + `c cr b r br name 'a 'static 'x' '\'' b'\x7d' "é }"suffix r##"é }"##suffix b"}"suffix br#"}"#suffix 1r 1br 1cr 1e+2 1..=2 :: => -> () [] {}` + " }\n" + rustFix + rustTests
			compileRustEdition(t, edition, source)
			module, err := RustTestContent([]byte(source))
			if err != nil || string(module) != strings.TrimSuffix(rustTests, "\n") {
				t.Fatalf("supported atoms: module %q, error %v", module, err)
			}
		})
	}
}

func TestRustOldEditionCBoundary(t *testing.T) {
	rustOptions(t)
	for _, edition := range []string{"2015", "2018"} {
		for _, hashes := range []string{"", "#", "##"} {
			t.Run(edition+hashes, func(t *testing.T) {
				source := fmt.Sprintf("macro_rules! m { ($($t:tt)*) => {} }\nm!{ cr%s\"\\\"%s }\n#[cfg(test)] mod fake {\n\"%s }\npub mod wrap { pub fn prod() -> i32 { 2 } // \"%s\n#[cfg(test)] mod tests { #[test] fn checks() { assert_eq!(super::prod(), 2); } }\n}\n", hashes, hashes, hashes, hashes)
				compileRustEdition(t, edition, source)
				if module, err := RustTestContent([]byte(source)); err == nil {
					t.Fatalf("accepted fake module containing production: %s", module)
				}
				changed := strings.ReplaceAll(source, "{ 2 }", "{ 3 }")
				if projected, _, _, err := projectRust([]byte(source), []byte(changed)); err == nil || projected != nil {
					t.Fatalf("unsafe projection returned bytes: %q, %v", projected, err)
				}
			})
		}
	}
}

func compileRustEdition(t *testing.T, edition, source string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "session.rs", source)
	binary := filepath.Join(dir, "checks")
	cmd := exec.CommandContext(t.Context(), "rustc", "--edition="+edition, "--test", filepath.Join(dir, "session.rs"), "-o", binary)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiler rejected corpus: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(t.Context(), binary).CombinedOutput(); err != nil {
		t.Fatalf("compiled corpus failed: %v\n%s", err, out)
	}
}
