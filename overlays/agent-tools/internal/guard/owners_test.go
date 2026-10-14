package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// TestOwners covers worktrees a plan gives an owner: the file tools and
// shell edits of any other agent there are denied, the owner's pass.
func TestOwners(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
	config := filepath.Join(root, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)

	main, linked := filepath.Join(root, "main"), filepath.Join(root, "linked")
	os.Mkdir(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	made, err := c.New(plans.NewOptions{Title: "X", Slug: "x"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Open(made.Plan)
	if _, err := p.SetOwner(linked, "a1"); err != nil {
		t.Fatal(err)
	}

	for _, e := range []struct {
		deny bool
		json string
	}{
		{false, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"` + linked + `/a.go"}}`},
		{true, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"` + linked + `/a.go"}}`},
		{true, `{"tool_name":"Write","tool_input":{"file_path":"` + linked + `/sub/new.go"}}`},
		{false, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"` + main + `/a.go"}}`},
		{true, `{"tool_name":"Bash","agent_id":"a2","cwd":"` + linked + `","tool_input":{"command":"echo x > new.txt"}}`},
		{true, `{"tool_name":"Bash","agent_id":"a2","cwd":"` + main + `","tool_input":{"command":"cd ../linked && sed -i '' s/a/b/ a.go"}}`},
		{false, `{"tool_name":"Bash","agent_id":"a2","cwd":"` + linked + `","tool_input":{"command":"cat a.go > /dev/null"}}`},
		{false, `{"tool_name":"Bash","agent_id":"a1","cwd":"` + linked + `","tool_input":{"command":"echo x > new.txt"}}`},
		{true, `{"tool_name":"apply_patch","turn_id":"t","agent_id":"a2","cwd":"` + linked + `","tool_input":{"command":"*** Begin Patch\n*** Add File: b.go\n+x\n*** End Patch\n"}}`},
		{false, `{"hook_type":"preToolCall","server":"eca","agent":"code","tool_name":"write_file","tool_input":{"path":"` + linked + `/a.go"}}`},
	} {
		var out bytes.Buffer
		Main([]string{"--baseline"}, strings.NewReader(e.json), &out, io.Discard)
		denied := strings.Contains(out.String(), `"deny"`)
		if denied != e.deny {
			t.Errorf("%s: deny=%v, output %s", e.json, denied, out.String())
		}
		if denied && (!strings.Contains(out.String(), "a1") || !strings.Contains(out.String(), "agent-plan own")) {
			t.Errorf("%s: the reason does not name the owner and the hand-over: %s", e.json, out.String())
		}
	}
}
