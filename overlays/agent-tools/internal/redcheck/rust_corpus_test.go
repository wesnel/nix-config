package redcheck

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustTokenCorpus(t *testing.T) {
	rustOptions(t)
	macro := "macro_rules! m { ($($t:tt)*) => {} }\n"
	supported := macro + `m! {
    name r br cr b c fn type _one
    0 1_2 1u8 0xffu8 0b1u8 0o7u8 1. 1.0 1e+2 1.0e-2
    1..2 1..=2 1u8-r#"#[cfg(test)] mod fake {}"#
    'a 'static 'x' '\'' '\u{7d}' b'\x7d'
    "é }"suffix r##"é }"##suffix b"}"suffix br#"}"#suffix
    'x'suffix b'x'suffix
    #"\" } #[cfg(test)] mod fake {"#
    :: => -> + - * / % ! # $ & | ^ ~ ? @ ; , . () [] {}
}
// Unicode comments café 🦀
/// Production documentation.
` + rustFix + rustTests
	for _, tc := range []struct {
		name, source, edition string
		refused               bool
	}{
		{"supported atoms", supported, "2021", false},
		{"reserved prefix older edition", macro + "m! { unknown\"\\\" } #[cfg(test)] mod fake {\" }\n" + rustFix + rustTests, "2018", false},
		{"raw identifier", macro + "m! { r#type }\n" + rustFix + rustTests, "2021", true},
		{"raw lifetime", macro + "m! { 'r#type }\n" + rustFix + rustTests, "2021", true},
		{"Unicode identifier", "fn café() {}\n" + rustFix + rustTests, "2021", true},
		{"Unicode whitespace", "\u0085\u200e\u200f\u2028\u2029" + rustFix + rustTests, "2021", true},
		{"BOM", "\ufeff" + rustFix + rustTests, "2021", true},
		{"shebang", "#!/usr/bin/env rust-script\n" + rustFix + rustTests, "2021", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "session.rs", tc.source)
			binary := filepath.Join(dir, "checks")
			cmd := exec.CommandContext(t.Context(), "rustc", "--edition="+tc.edition, "--test", filepath.Join(dir, "session.rs"), "-o", binary)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compiler rejected corpus: %v\n%s", err, out)
			}
			if out, err := exec.CommandContext(t.Context(), binary).CombinedOutput(); err != nil {
				t.Fatalf("compiled corpus failed: %v\n%s", err, out)
			}
			module, err := RustTestContent([]byte(tc.source))
			if tc.refused {
				if err == nil {
					t.Fatal("accepted unsupported boundary")
				}
			} else if err != nil || string(module) != strings.TrimSuffix(rustTests, "\n") {
				t.Fatalf("module %q, error %v", module, err)
			}
		})
	}
}
