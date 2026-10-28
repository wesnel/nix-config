package pinscan

import (
	"strings"
	"testing"
)

func TestFlakeInputsChanged(t *testing.T) {
	old := `{ inputs = { x = { url = "github:o/r"; inputs.nixpkgs.follows = "nixpkgs"; flake = false; }; }; outputs = _: {}; }`
	for _, tc := range []struct {
		name, old, next string
		want            bool
	}{
		{"output option", old, strings.Replace(old, "outputs = _: {};", "outputs = _: { options.org-roam.directory = {}; };", 1), false},
		{"spacing and comments", old, "# inputs.decoy.url = \"evil\";\n" + old + "\n/* tail */", false},
		{"url", old, strings.Replace(old, "github:o/r", "github:o/other", 1), true},
		{"follows", old, strings.Replace(old, `follows = "nixpkgs"`, `follows = "other"`, 1), true},
		{"flake", old, strings.Replace(old, "flake = false", "flake = true", 1), true},
		{"unknown literal field", old, strings.Replace(old, "flake = false;", "flake = false; submodules = true;", 1), true},
		{"dotted declarations", `{ inputs.x.url = "github:o/r"; outputs = _: {}; }`, `{ inputs.x.url = "github:o/r"; outputs = _: { a = true; }; }`, false},
		{"no inputs", `{ outputs = _: {}; }`, `{ outputs = _: { a = true; }; }`, false},
		{"output expressions", old, strings.Replace(old, "outputs = _: {};", `outputs = _: let a = "${builtins.toJSON { x = "}"; }}"; b = ''text ''${literal} ${a}''; in with {}; assert true; { value = (with {}; a); };`, 1), false},
		{"static output path", old, strings.Replace(old, "outputs = _: {};", "outputs = _: import ./overlays/default.nix;", 1), false},
		{"identifier apostrophes", old, strings.Replace(old, "outputs = _: {};", `outputs = _: let a'' = "unused"; in {};`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FlakeInputsChanged(tc.old, tc.next); got != tc.want {
				t.Errorf("changed = %v, want %v", got, tc.want)
			}
		})
	}
	for name, text := range map[string]string{
		"rec root":                `rec { inputs.x.url = "github:o/r"; outputs = _: {}; }`,
		"let root":                `let source = "github:o/r"; in { inputs.x.url = source; outputs = _: {}; }`,
		"assert root":             `assert true; { inputs = {}; outputs = _: {}; }`,
		"with root":               `with {}; { inputs = {}; outputs = _: {}; }`,
		"inherited inputs":        `{ inherit inputs; outputs = _: {}; }`,
		"inherited input field":   `{ inputs.x = { inherit url; }; outputs = _: {}; }`,
		"reference":               `{ inputs.x.url = source; outputs = _: {}; }`,
		"interpolation":           `{ inputs.x.url = "github:${owner}/r"; outputs = _: {}; }`,
		"concatenation":           `{ inputs.x.url = "github:" + "o/r"; outputs = _: {}; }`,
		"computed inputs":         `{ inputs = (builtins.getFlake "github:o/r").inputs; outputs = _: {}; }`,
		"rec inputs":              `{ inputs = rec { x.url = "github:o/r"; }; outputs = _: {}; }`,
		"dynamic input name":      `{ inputs.${name}.url = "github:o/r"; outputs = _: {}; }`,
		"dynamic root name":       `{ inputs = {}; ${name} = {}; outputs = _: {}; }`,
		"quoted root name":        `{ "inputs" = {}; outputs = _: {}; }`,
		"duplicate input":         `{ inputs.x.url = "github:o/r"; inputs.x.url = "github:o/r"; outputs = _: {}; }`,
		"overlapping input":       `{ inputs.x = {}; inputs.x.url = "github:o/r"; outputs = _: {}; }`,
		"nested duplicate":        `{ inputs = { x.url = "github:o/r"; x.url = "github:o/r"; }; outputs = _: {}; }`,
		"merge":                   `{ inputs = {}; outputs = _: {}; } // { inputs.x.url = "github:o/r"; }`,
		"input merge":             `{ inputs = {} // {}; outputs = _: {}; }`,
		"unclosed set":            `{ inputs = {}; outputs = _: {};`,
		"unclosed string":         `{ inputs.x.url = "github:o/r; outputs = _: {}; }`,
		"unclosed comment":        `{ inputs = {}; outputs = _: {}; } /*`,
		"mismatched output group": `{ inputs = {}; outputs = _: ([)]; }`,
		"empty output":            `{ inputs = {}; outputs = ; }`,
		"empty source":            "",
	} {
		t.Run(name, func(t *testing.T) {
			next := strings.Replace(text, "outputs = _: {};", "outputs = _: { option = true; };", 1)
			if !FlakeInputsChanged(text, next) || !FlakeInputsChanged(old, text) || !FlakeInputsChanged(text, old) {
				t.Error("unsupported input/root syntax must require review on either side")
			}
		})
	}
}

func TestFlakeLexicalBoundaries(t *testing.T) {
	for name, old := range map[string]string{
		"URI quotes hide input":   `{ description = https://a''; inputs.x.url = "github:o/OLD"; nixConfig.extra-substituters = https://b''; outputs = _: {}; }`,
		"URI dot quote":           `{ description = https://a.''; inputs.x.url = "github:o/OLD"; nixConfig.extra-substituters = https://b.''; outputs = _: {}; }`,
		"URI dot comment":         `{ description = https://a./*; inputs.x.url = "github:o/OLD"; nixConfig.extra-substituters = https://b.*/; outputs = _: {}; }`,
		"URI comment delimiter":   `{ description = https:/*; inputs.x.url = "github:o/OLD"; nixConfig.extra-substituters = https:*/; outputs = _: {}; }`,
		"indented escape":         `{ inputs.x.url = "github:o/OLD"; description = ''hello ''\n world''; outputs = _: {}; }`,
		"double dollar string":    `{ inputs.x.url = "github:o/OLD"; description = "$${}"; outputs = _: {}; }`,
		"double dollar indent":    `{ inputs.x.url = "github:o/OLD"; description = ''$${}''; outputs = _: {}; }`,
		"interpolated path":       `{ inputs.x.url = "github:o/OLD"; description = ./a/${name}; outputs = _: {}; }`,
		"unsupported path quote":  `{ inputs.x.url = "github:o/OLD"; description = ./a''quoted''; outputs = _: {}; }`,
		"unsupported lookup path": `{ inputs.x.url = "github:o/OLD"; description = <nixpkgs>; outputs = _: {}; }`,
	} {
		t.Run(name, func(t *testing.T) {
			if !FlakeInputsChanged(old, strings.Replace(old, "github:o/OLD", "github:o/NEW", 1)) {
				t.Error("source-only input change was hidden by lexical boundaries")
			}
			if !FlakeInputsChanged(old, old) {
				t.Error("unsupported lexical syntax must remain conservative")
			}
		})
	}
}
