package export_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/plans"
)

// planIDJS runs the page's PLAN_ID, cut from its script, over the IDs in
// the JSON file argv[3], and prints which it takes.
const planIDJS = `"use strict";
const fs = require("fs");
const page = fs.readFileSync(process.argv[2], "utf8");
const ids = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const line = page.split("\n").find((l) => l.startsWith("  const PLAN_ID = /"));
if (!line) throw new Error("the script has no PLAN_ID");
const PLAN_ID = new Function("return " + line.slice("  const PLAN_ID = ".length).replace(/;$/, ""))();
process.stdout.write(JSON.stringify(ids.map((id) => PLAN_ID.test(id))));
`

// The page lists a plan only where its PLAN_ID takes the plan's ID: one
// agent-plan new names now, and one named the way it did before, but
// nothing else. The package build has node; elsewhere the test needs it
// on PATH.
func TestPagePlanID(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "T", Slug: "add-loop"}, time.Date(2026, 10, 14, 13, 5, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id   string
		take bool
	}{
		{made.ID, true},
		{"2026-10-14-add-loop.box", true},
		{"20261014-add-loop.box", false},
		{"2026-10-14-add-loop", false},
		{"20261014130500-Add-loop.box", false},
		{"20261014130500-add-loop.box.P1", false},
	}
	var ids []string
	for _, c := range cases {
		ids = append(ids, c.id)
	}
	dir := t.TempDir()
	runner, pageFile, idsFile := filepath.Join(dir, "run.js"), filepath.Join(dir, "page.html"), filepath.Join(dir, "ids.json")
	data, _ := json.Marshal(ids)
	for file, content := range map[string][]byte{runner: []byte(planIDJS), pageFile: agentplans.Page, idsFile: data} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, runner, pageFile, idsFile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(cases) {
		t.Fatalf("node's verdicts %s: %v", out, err)
	}
	for i, c := range cases {
		if got[i] != c.take {
			t.Errorf("PLAN_ID takes %q: %v, want %v", c.id, got[i], c.take)
		}
	}
}
