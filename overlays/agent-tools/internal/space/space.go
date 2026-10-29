// Package space mirrors the Agent plans page to a ChatGPT Space page, as
// one visualization block, through `codex app-server` and the ChatGPT
// Space plugin connected to the operator's Codex. It calls the plugin's
// tools directly, in a thread of its own, and starts no model turn.
package space

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// Title is the visualization's title, and its embed's alt text.
const Title = "Agent plans"

// The plugin's tools, by the names its MCP server, codex_apps, lists them
// under: codex calls a tool by that name.
const (
	server    = "codex_apps"
	readPage  = "chatgpt_space.read_page"
	writePage = "chatgpt_space.create_page_visualization"
)

// State is what the publisher records, in agentplans.SpaceStateFile,
// which the guard keeps sessions from writing: the block it owns is the
// only one it ever replaces.
type State struct {
	// Page is the page Block is on.
	Page string `json:"page"`
	// Thread is the Codex thread the tools are called in. A thread is
	// kept from one app-server to the next only once it holds an item.
	Thread string `json:"thread"`
	// Block is the block the publisher made on Page.
	Block string `json:"block,omitempty"`
	// Published is the SHA-256 of the HTML Block was last given.
	Published string `json:"published,omitempty"`
}

// Result is what a publish did: "created" a block, "replaced" its block,
// or nothing, the HTML "unchanged".
type Result struct {
	Status string `json:"status"`
	Page   string `json:"page"`
	Block  string `json:"block"`
}

// Publisher publishes through the app-server Dial starts.
type Publisher struct {
	// Dial starts an app-server and returns its stdio; Close ends it.
	Dial func(context.Context) (io.ReadWriteCloser, error)
	// StateFile holds State.
	StateFile string
	// Dir is the thread's working directory.
	Dir string
}

// Codex starts `codex app-server` in dir, found on PATH.
func Codex(dir string) func(context.Context) (io.ReadWriteCloser, error) {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
		cmd.Dir = dir
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return process{out, in, cmd}, nil
	}
}

type process struct {
	io.Reader
	in  io.WriteCloser
	cmd *exec.Cmd
}

func (p process) Write(b []byte) (int, error) { return p.in.Write(b) }

// Close ends the app-server, which exits once its input does.
func (p process) Close() error {
	p.in.Close()
	return p.cmd.Wait()
}

// Publish puts html on page: in its block where State has one there, or
// in a new block at the page's end, which it records. HTML the block
// already holds starts nothing.
func (p Publisher) Publish(ctx context.Context, page string, html []byte) (Result, error) {
	s, err := p.load()
	if err != nil {
		return Result{}, err
	}
	if s.Page != page {
		s = State{Page: page, Thread: s.Thread}
	}
	sum := sha256.Sum256(html)
	hash := hex.EncodeToString(sum[:])
	if s.Block != "" && s.Published == hash {
		return Result{"unchanged", page, s.Block}, nil
	}
	conn, err := p.Dial(ctx)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	c := newClient(conn)
	if err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "plan-dash", "version": "1"},
		// No browser opens for a login: there is no one at the devbox.
		"capabilities": map[string]bool{"explicitGatewayOauth": true}}, nil); err != nil {
		return Result{}, err
	}
	if err := c.notify("initialized"); err != nil {
		return Result{}, err
	}
	if s.Thread == "" {
		if s.Thread, err = c.startThread(p.Dir); err != nil {
			return Result{}, err
		}
		if err := p.save(s); err != nil {
			return Result{}, err
		}
	} else if err := c.resumeThread(s.Thread); err != nil {
		return Result{}, err
	}
	args := map[string]any{"page_id": page, "title": Title, "html": string(html)}
	status := "created"
	if s.Block != "" {
		hash, sequence, err := c.blockHash(s.Thread, page, s.Block)
		if err != nil {
			return Result{}, err
		}
		args["replace_block"] = map[string]string{"block_id": s.Block, "expected_hash": hash}
		if sequence != nil {
			args["base_sequence"] = *sequence
		}
		status = "replaced"
	}
	b, err := c.visualize(s.Thread, args, s.Block)
	if err != nil {
		return Result{}, err
	}
	s.Block, s.Published = b, hex.EncodeToString(sum[:])
	if err := p.save(s); err != nil {
		return Result{}, err
	}
	return Result{status, page, b}, nil
}

// id is the shape of an ID the publisher records.
var id = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

func (p Publisher) load() (State, error) {
	var s State
	data, err := os.ReadFile(p.StateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", p.StateFile, err)
	}
	return s, nil
}

// save replaces StateFile by a rename.
func (p Publisher) save(s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.StateFile), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.StateFile), "."+filepath.Base(p.StateFile)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), p.StateFile)
}

// client speaks the app-server's JSON-RPC, a message a line.
type client struct {
	in   *bufio.Scanner
	out  *json.Encoder
	next int
}

func newClient(conn io.ReadWriter) *client {
	in := bufio.NewScanner(conn)
	in.Buffer(nil, 8<<20)
	return &client{in: in, out: json.NewEncoder(conn)}
}

func (c *client) notify(method string) error {
	return c.out.Encode(map[string]string{"method": method})
}

// call sends a request and reads its result into result, skipping
// notifications. A request from the app-server, such as an approval or a
// login, stops it: the publisher answers none.
func (c *client) call(method string, params, result any) error {
	c.next++
	if err := c.out.Encode(map[string]any{"id": c.next, "method": method, "params": params}); err != nil {
		return err
	}
	for c.in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(c.in.Bytes(), &m); err != nil {
			return fmt.Errorf("codex app-server: %w", err)
		}
		switch {
		case m.Method != "" && m.ID != nil:
			return fmt.Errorf("codex app-server asked %q during %s, which plan-dash does not answer: settle it in Codex", m.Method, method)
		case m.Method != "":
			continue
		case string(m.ID) != fmt.Sprint(c.next):
			return fmt.Errorf("codex app-server answered request %s during %s", m.ID, method)
		case m.Error != nil:
			return fmt.Errorf("codex app-server refused %s: %s", method, m.Error.Message)
		case result == nil:
			return nil
		}
		return json.Unmarshal(m.Result, result)
	}
	if err := c.in.Err(); err != nil {
		return err
	}
	return fmt.Errorf("codex app-server ended during %s", method)
}

type thread struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

// startThread starts a thread and injects a note into it, which writes the
// thread to disk without a turn, so a later app-server can resume it.
func (c *client) startThread(dir string) (string, error) {
	var t thread
	if err := c.call("thread/start", map[string]any{"cwd": dir, "ephemeral": false}, &t); err != nil {
		return "", err
	}
	if !id.MatchString(t.Thread.ID) {
		return "", fmt.Errorf("codex app-server started a thread with no usable ID")
	}
	note := map[string]any{"type": "message", "role": "developer", "content": []any{map[string]string{"type": "input_text",
		"text": "plan-dash space calls the ChatGPT Space tools in this thread to publish the Agent plans page. No turn runs here."}}}
	if err := c.call("thread/inject_items", map[string]any{"threadId": t.Thread.ID, "items": []any{note}}, nil); err != nil {
		return "", err
	}
	return t.Thread.ID, nil
}

func (c *client) resumeThread(want string) error {
	var t thread
	if err := c.call("thread/resume", map[string]any{"threadId": want, "excludeTurns": true}, &t); err != nil {
		return err
	}
	if t.Thread.ID != want {
		return fmt.Errorf("codex app-server resumed another thread than %s", want)
	}
	return nil
}

// spaceError is a Space tool's error, as its result's structuredContent
// gives it at the top or in its edit receipt. Only its codes are reported:
// its message is the service's text.
type spaceError struct {
	Code         string `json:"code"`
	CommitStatus string `json:"commit_status"`
}

func (e *spaceError) String() string { return e.Code + ", " + e.CommitStatus }

// tool calls a Space tool in thread and reads its structuredContent into
// result, or fails with the tool's error.
func (c *client) tool(thread, name string, args map[string]any, result any) error {
	var r struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := c.call("mcpServer/tool/call", map[string]any{"threadId": thread, "server": server, "tool": name, "arguments": args}, &r); err != nil {
		return err
	}
	var failed struct {
		Error *spaceError `json:"error"`
		Edit  struct {
			Error *spaceError `json:"error"`
		} `json:"edit"`
	}
	json.Unmarshal(r.StructuredContent, &failed)
	switch {
	case failed.Error != nil:
		return fmt.Errorf("%s failed: %s", name, failed.Error)
	case failed.Edit.Error != nil:
		return fmt.Errorf("%s failed: %s", name, failed.Edit.Error)
	case r.IsError:
		return fmt.Errorf("%s failed", name)
	}
	if err := json.Unmarshal(r.StructuredContent, result); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// blockHash reads block's hash on page, and the page's sequence where it
// gives one, as replacing the block needs them.
func (c *client) blockHash(thread, page, block string) (string, *int64, error) {
	var r struct {
		Content *struct {
			PageID   string `json:"page_id"`
			Sequence *int64 `json:"through_sequence"`
			Blocks   []struct {
				ID   string `json:"id"`
				Hash string `json:"hash"`
			} `json:"blocks"`
		} `json:"content"`
	}
	if err := c.tool(thread, readPage, map[string]any{"page_id": page, "block_ids": []string{block}}, &r); err != nil {
		return "", nil, err
	}
	if r.Content == nil || r.Content.PageID != page {
		return "", nil, fmt.Errorf("%s returned no content of page %s", readPage, page)
	}
	for _, b := range r.Content.Blocks {
		if b.ID == block && b.Hash != "" {
			return b.Hash, r.Content.Sequence, nil
		}
	}
	return "", nil, fmt.Errorf("block %s is not on page %s: the operator removes the Space state to make a new one", block, page)
}

// visualize uploads the HTML as args say and returns the block that holds
// it, from the edit's receipt: the one block it made, or, replacing, the
// block replaced.
func (c *client) visualize(thread string, args map[string]any, replacing string) (string, error) {
	var r struct {
		Edit struct {
			Results []struct {
				Status   string   `json:"status"`
				Code     *string  `json:"error_code"`
				Ignored  *string  `json:"ignored_reason"`
				Created  []string `json:"created_block_ids"`
				Affected []struct {
					ID string `json:"block_id"`
				} `json:"affected_blocks"`
			} `json:"operation_results"`
		} `json:"edit"`
	}
	if err := c.tool(thread, writePage, args, &r); err != nil {
		return "", err
	}
	if len(r.Edit.Results) != 1 {
		return "", fmt.Errorf("%s gave %d receipts for one edit", writePage, len(r.Edit.Results))
	}
	op := r.Edit.Results[0]
	switch {
	case op.Status != "" && op.Status != "applied", op.Code != nil, op.Ignored != nil:
		return "", fmt.Errorf("%s did not apply its edit", writePage)
	case len(op.Created) == 1 && id.MatchString(op.Created[0]):
		return op.Created[0], nil
	case replacing != "" && len(op.Created) == 0 && len(op.Affected) == 1 && op.Affected[0].ID == replacing:
		return replacing, nil
	}
	return "", fmt.Errorf("%s's receipt names no one block it made or replaced", writePage)
}
