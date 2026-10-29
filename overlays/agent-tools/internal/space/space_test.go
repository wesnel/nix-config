package space

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

// block is one block of a fake Space page.
type block struct{ id, hash, markdown string }

// fakeCodex stands in for `codex app-server` and the ChatGPT Space plugin
// behind it, which is a service of the operator's account. It answers as
// codex 0.165 does and with the shapes of the Space tools' schemas in
// codex's tool cache: a thread started without a turn survives to another
// connection only once items are injected into it, a tool is called
// through a thread loaded in the same connection, read_page returns the
// blocks asked for, and create_page_visualization appends a block or
// replaces one whose hash it is given.
type fakeCodex struct {
	pages   map[string][]block
	threads map[string]bool // materialized
	seq     int
	n       int
	// request, where set, is a request the fake sends the client before
	// answering a tool call, such as an approval.
	request string
	// toolError, where set, is the error code create_page_visualization
	// fails with.
	toolError string
	dials     int
}

func newFake(pages ...string) *fakeCodex {
	f := &fakeCodex{pages: map[string][]block{}, threads: map[string]bool{}}
	for _, p := range pages {
		// Each page holds a block of the operator's, which no publish touches.
		f.pages[p] = []block{{"operator", "h-operator", "Notes of the operator's"}}
	}
	return f
}

func (f *fakeCodex) dial(ctx context.Context) (io.ReadWriteCloser, error) {
	f.dials++
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	go f.serve(serverIn, serverOut)
	return struct {
		io.Reader
		io.Writer
		io.Closer
	}{clientIn, clientOut, clientOut}, nil
}

func (f *fakeCodex) serve(in io.ReadCloser, out io.WriteCloser) {
	defer out.Close()
	loaded := map[string]bool{} // threads loaded in this connection
	enc := json.NewEncoder(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(nil, 4<<20)
	for scanner.Scan() {
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.ID == nil {
			continue // a notification
		}
		result, rpcErr := f.handle(req.Method, req.Params, loaded, enc)
		if rpcErr != "" {
			enc.Encode(map[string]any{"id": *req.ID, "error": map[string]any{"code": -32600, "message": rpcErr}})
			continue
		}
		enc.Encode(map[string]any{"id": *req.ID, "result": result})
	}
}

func (f *fakeCodex) handle(method string, raw json.RawMessage, loaded map[string]bool, enc *json.Encoder) (any, string) {
	var p map[string]json.RawMessage
	json.Unmarshal(raw, &p)
	str := func(k string) string { var s string; json.Unmarshal(p[k], &s); return s }
	switch method {
	case "initialize":
		enc.Encode(map[string]any{"method": "remoteControl/status/changed", "params": map[string]any{"status": "disabled"}})
		return map[string]any{"userAgent": "codex/0.165.0"}, ""
	case "thread/start":
		f.n++
		id := fmt.Sprintf("thread-%d", f.n)
		f.threads[id] = false
		loaded[id] = true
		return map[string]any{"thread": map[string]any{"id": id, "ephemeral": false}}, ""
	case "thread/inject_items":
		id := str("threadId")
		if !loaded[id] {
			return nil, "thread not found: " + id
		}
		f.threads[id] = true
		return map[string]any{}, ""
	case "thread/resume":
		id := str("threadId")
		if !f.threads[id] {
			return nil, "no rollout found for thread id " + id
		}
		loaded[id] = true
		return map[string]any{"thread": map[string]any{"id": id}}, ""
	case "mcpServer/tool/call":
		if !loaded[str("threadId")] {
			return nil, "thread not found: " + str("threadId")
		}
		if str("server") != "codex_apps" {
			return nil, "unknown MCP server '" + str("server") + "'"
		}
		if f.request != "" {
			enc.Encode(map[string]any{"id": 99, "method": f.request, "params": map[string]any{}})
		}
		var args map[string]json.RawMessage
		json.Unmarshal(p["arguments"], &args)
		switch str("tool") {
		case "chatgpt_space.read_page":
			return f.readPage(args), ""
		case "chatgpt_space.create_page_visualization":
			return f.createVisualization(args), ""
		}
		return nil, "tool call failed for `codex_apps/" + str("tool") + "`"
	}
	return nil, "unknown method " + method
}

func toolResult(structured any, isError bool) any {
	return map[string]any{"content": []any{}, "structuredContent": structured, "isError": isError}
}

func (f *fakeCodex) readPage(args map[string]json.RawMessage) any {
	var page string
	var ids []string
	json.Unmarshal(args["page_id"], &page)
	json.Unmarshal(args["block_ids"], &ids)
	blocks, ok := f.pages[page]
	if !ok {
		return toolResult(map[string]any{"error": map[string]any{"code": "page_not_found", "message": "no such page"}}, true)
	}
	selected, missing := []any{}, []string{}
	for _, id := range ids {
		found := false
		for _, b := range blocks {
			if b.id == id {
				selected = append(selected, map[string]any{"id": b.id, "hash": b.hash, "kind": "markdown", "markdown": b.markdown})
				found = true
			}
		}
		if !found {
			missing = append(missing, id)
		}
	}
	return toolResult(map[string]any{
		"content":   map[string]any{"page_id": page, "schema_version": 2, "title": "Dashboard", "title_hash": "t", "through_sequence": f.seq, "blocks": selected},
		"metadata":  map[string]any{"page_id": page, "url": "https://chatgpt.com/p/" + page},
		"selection": map[string]any{"mode": "block_ids", "selection_complete": len(missing) == 0, "missing_block_ids": missing},
	}, false)
}

func (f *fakeCodex) createVisualization(args map[string]json.RawMessage) any {
	for k := range args {
		if k != "page_id" && k != "html" && k != "title" && k != "replace_block" && k != "base_sequence" && k != "after_block_id" {
			return toolResult(map[string]any{"error": map[string]any{"code": "invalid_arguments"}}, true)
		}
	}
	var page, html, title string
	json.Unmarshal(args["page_id"], &page)
	json.Unmarshal(args["html"], &html)
	json.Unmarshal(args["title"], &title)
	blocks, ok := f.pages[page]
	if !ok || html == "" || title == "" {
		return toolResult(map[string]any{"error": map[string]any{"code": "invalid_arguments"}}, true)
	}
	if f.toolError != "" {
		return toolResult(map[string]any{"edit": map[string]any{"error": map[string]any{"code": f.toolError, "commit_status": "not_committed", "message": "x"}}}, true)
	}
	f.seq++
	file := fmt.Sprintf("file_%d", f.seq)
	b := block{fmt.Sprintf("blk-%d", f.seq), fmt.Sprintf("h-%d", f.seq), "![" + title + "](visualize:" + file + ")"}
	created := []string{b.id}
	if raw, ok := args["replace_block"]; ok && string(raw) != "null" {
		var r struct {
			BlockID      string `json:"block_id"`
			ExpectedHash string `json:"expected_hash"`
		}
		json.Unmarshal(raw, &r)
		i := -1
		for j, old := range blocks {
			if old.id == r.BlockID {
				i = j
			}
		}
		if i < 0 || blocks[i].hash != r.ExpectedHash {
			return toolResult(map[string]any{"edit": map[string]any{"error": map[string]any{"code": "edit_conflict", "commit_status": "not_committed", "message": "x"}}}, true)
		}
		b.id, created = r.BlockID, []string{}
		blocks[i] = b
	} else {
		blocks = append(blocks, b)
	}
	f.pages[page] = blocks
	return toolResult(map[string]any{"page_id": page, "title": title, "file_reference": file, "markdown": b.markdown,
		"edit": map[string]any{"new_sequence": f.seq, "operation_results": []any{map[string]any{
			"operation_index": 0, "status": "applied", "ignored_reason": nil, "error_code": nil,
			"affected_blocks": []any{map[string]any{"block_id": b.id, "hash": b.hash}}, "created_block_ids": created,
			"continuation_position": nil, "preceding_block_id": nil}}}}, false)
}

// ours is the page's blocks but the operator's.
func (f *fakeCodex) ours(page string) []block {
	var out []block
	for _, b := range f.pages[page] {
		if b.id != "operator" {
			out = append(out, b)
		}
	}
	return out
}

func (f *fakeCodex) operatorKept(t *testing.T, page string) {
	t.Helper()
	if b := f.pages[page][0]; b != (block{"operator", "h-operator", "Notes of the operator's"}) {
		t.Errorf("the operator's block changed: %+v", b)
	}
}

func readState(t *testing.T, file string) State {
	t.Helper()
	var s State
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func publisher(t *testing.T, f *fakeCodex) Publisher {
	t.Helper()
	dir := t.TempDir()
	return Publisher{Dial: f.dial, StateFile: filepath.Join(dir, "space.json"), Dir: dir}
}

// The first run makes a thread and a block at the page's end and records
// both; a later run with other HTML replaces that block alone, in the same
// thread; one with the same HTML starts nothing.
func TestPublish(t *testing.T) {
	f := newFake("page_a")
	p := publisher(t, f)
	got, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>"))
	if err != nil {
		t.Fatal(err)
	}
	s := readState(t, p.StateFile)
	if got.Status != "created" || len(f.ours("page_a")) != 1 || s.Page != "page_a" || s.Thread != "thread-1" ||
		s.Block != f.ours("page_a")[0].id || got.Block != s.Block {
		t.Fatalf("first run: %+v, state %+v, page %+v", got, s, f.pages["page_a"])
	}
	first := s.Block

	if got, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>")); err != nil || got.Status != "unchanged" || f.dials != 1 {
		t.Errorf("same HTML: %+v %v, %d dials", got, err, f.dials)
	}

	got, err = p.Publish(t.Context(), "page_a", []byte("<p>two</p>"))
	if err != nil {
		t.Fatal(err)
	}
	s = readState(t, p.StateFile)
	if ours := f.ours("page_a"); got.Status != "replaced" || len(ours) != 1 || ours[0].id != first || ours[0].hash == "h-1" ||
		s.Block != first || s.Thread != "thread-1" || f.n != 1 {
		t.Errorf("second run: %+v, state %+v, page %+v, %d threads", got, s, f.pages["page_a"], f.n)
	}
	f.operatorKept(t, "page_a")
}

// A page set to another starts a block there, in the same thread, and
// leaves the old page as it was.
func TestPublishNewPage(t *testing.T) {
	f := newFake("page_a", "page_b")
	p := publisher(t, f)
	if _, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>")); err != nil {
		t.Fatal(err)
	}
	old := f.ours("page_a")
	got, err := p.Publish(t.Context(), "page_b", []byte("<p>one</p>"))
	s := readState(t, p.StateFile)
	if err != nil || got.Status != "created" || len(f.ours("page_b")) != 1 || s.Page != "page_b" ||
		s.Block != f.ours("page_b")[0].id || s.Thread != "thread-1" || len(f.ours("page_a")) != 1 || f.ours("page_a")[0] != old[0] {
		t.Errorf("%+v %v, state %+v, pages %+v", got, err, s, f.pages)
	}
	f.operatorKept(t, "page_b")
}

// What stops a publish records nothing new and makes no second block.
func TestPublishRefuses(t *testing.T) {
	setUp := func(t *testing.T) (*fakeCodex, Publisher, State) {
		f := newFake("page_a")
		p := publisher(t, f)
		if _, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>")); err != nil {
			t.Fatal(err)
		}
		return f, p, readState(t, p.StateFile)
	}
	for _, c := range []struct {
		name   string
		break_ func(*fakeCodex)
		want   string
	}{
		{"its block is gone", func(f *fakeCodex) { f.pages["page_a"] = f.pages["page_a"][:1] }, "is not on page"},
		{"codex asks the client something", func(f *fakeCodex) { f.request = "item/permissions/requestApproval" }, "item/permissions/requestApproval"},
		{"the edit conflicts", func(f *fakeCodex) { f.toolError = "edit_conflict" }, "edit_conflict"},
		{"its thread is gone", func(f *fakeCodex) { f.threads = map[string]bool{} }, "no rollout found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, p, before := setUp(t)
			c.break_(f)
			blocks := len(f.pages["page_a"])
			_, err := p.Publish(t.Context(), "page_a", []byte("<p>two</p>"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want %q", err, c.want)
			}
			if after := readState(t, p.StateFile); after != before || len(f.pages["page_a"]) > blocks {
				t.Errorf("state %+v after %+v, %d blocks after %d", after, before, len(f.pages["page_a"]), blocks)
			}
		})
	}
}

// Against the real `codex app-server`, in a CODEX_HOME of its own with no
// login: the thread the first run makes and records is the one a second
// app-server resumes, though no Space tool can run. It needs codex on
// PATH, and runs only with PLAN_DASH_REAL_CODEX=1, since codex tries the
// network.
func TestPublishRealCodexThread(t *testing.T) {
	if os.Getenv("PLAN_DASH_REAL_CODEX") != "1" {
		t.Skip("set PLAN_DASH_REAL_CODEX=1, with codex on PATH, to run it")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := t.TempDir()
	p := Publisher{Dial: Codex(dir), StateFile: filepath.Join(dir, "space.json"), Dir: dir}
	if _, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>")); err == nil {
		t.Fatal("published with no login")
	}
	first := readState(t, p.StateFile)
	_, err := p.Publish(t.Context(), "page_a", []byte("<p>one</p>"))
	if second := readState(t, p.StateFile); first.Thread == "" || second != first || err == nil ||
		!strings.Contains(err.Error(), "mcpServer/tool/call") {
		t.Errorf("first %+v, second %+v: %v", first, second, err)
	}
	t.Logf("thread %s; the tool call failed: %v", first.Thread, err)
}
