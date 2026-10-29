package export_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// byWorkstreamJS runs the page's BY_WORKSTREAM, cut from its script, over
// the plans in the JSON file argv[3], and prints each group as its
// workstream and the IDs of its plans.
const byWorkstreamJS = `"use strict";
const fs = require("fs");
const page = fs.readFileSync(process.argv[2], "utf8");
const plans = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const line = page.split("\n").find((l) => l.startsWith("  const BY_WORKSTREAM = "));
if (!line) throw new Error("the script has no BY_WORKSTREAM");
const BY_WORKSTREAM = new Function("return " + line.slice("  const BY_WORKSTREAM = ".length).replace(/;$/, ""))();
process.stdout.write(JSON.stringify(BY_WORKSTREAM(plans).map(([w, ps]) => [w, ps.map((p) => p.id)])));
`

// The page's index groups the plans, in the order it lists them, under
// each plan's workstream: a workstream where its first plan comes, the
// plans in none last. The package build has node; elsewhere the test
// needs it on PATH.
func TestPageWorkstreams(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	plans := []map[string]string{
		{"id": "d", "workstream": "Agent tooling"},
		{"id": "c", "workstream": ""},
		{"id": "b", "workstream": "Codex parity"},
		{"id": "a", "workstream": "Agent tooling"},
	}
	dir := t.TempDir()
	runner, pageFile, plansFile := filepath.Join(dir, "run.js"), filepath.Join(dir, "page.html"), filepath.Join(dir, "plans.json")
	data, _ := json.Marshal(plans)
	for file, content := range map[string][]byte{runner: []byte(byWorkstreamJS), pageFile: agentplans.Page, plansFile: data} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, runner, pageFile, plansFile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	if want := `[["Agent tooling",["d","a"]],["Codex parity",["b"]],["",["c"]]]`; string(out) != want {
		t.Errorf("groups %s, want %s", out, want)
	}
}
