package agentplans

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func SpaceStateFile() string { return filepath.Join(Dir(), "space", "publication.json") }

type SpaceReceipt struct {
	PageID       string    `json:"page_id"`
	BlockID      string    `json:"block_id"`
	SnapshotHash string    `json:"snapshot_hash"`
	BlockHash    string    `json:"block_hash"`
	Markdown     string    `json:"markdown"`
	VerifiedAt   time.Time `json:"verified_at"`
}

type spaceState struct {
	Published *SpaceReceipt `json:"published,omitempty"`
	Pending   *SpaceReceipt `json:"pending,omitempty"`
}

func loadSpaceState(file string) (spaceState, error) {
	var state spaceState
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if strictJSON(data, &state) != nil || state.Published == nil && state.Pending == nil {
		return state, fmt.Errorf("publication journal is invalid; operator reconciliation is required")
	}
	for _, p := range []*SpaceReceipt{state.Published, state.Pending} {
		if p != nil && (!spaceID.MatchString(p.PageID) || !spaceID.MatchString(p.BlockID) || p.SnapshotHash == "" || p.BlockHash == "" || p.Markdown != "" && !visualizationMarkdown(p.Markdown)) {
			return state, fmt.Errorf("publication journal has an invalid receipt")
		}
	}
	if p := state.Published; p != nil && (p.Markdown == "" || p.VerifiedAt.IsZero()) {
		return state, fmt.Errorf("publication journal has an unverified receipt")
	}
	return state, nil
}

// PublishSpace uses the operator's existing app-server thread and connected
// Pages plugin. It starts no turn, performs no login, grants no approval and
// exposes no arbitrary method, tool, endpoint or target to its caller.
func PublishSpace(ctx context.Context) (SpaceReceipt, error) {
	var empty SpaceReceipt
	target, err := LoadSpaceTarget()
	if err != nil {
		return empty, err
	}
	html, err := os.ReadFile(SnapshotFile())
	if err != nil {
		return empty, err
	}
	if err := ValidateSnapshot(html); err != nil {
		return empty, err
	}
	if err := os.MkdirAll(filepath.Dir(SpaceStateFile()), 0o700); err != nil {
		return empty, err
	}
	lock, err := os.OpenFile(filepath.Join(Dir(), "space", "publish.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return empty, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return empty, fmt.Errorf("Space publisher is already running: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
	input, err := cmd.StdinPipe()
	if err != nil {
		return empty, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return empty, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return empty, err
	}
	defer func() { input.Close(); cancel(); _ = cmd.Wait() }()
	return publishSpace(target, html, newSpaceRPC(output, input), SpaceStateFile(), time.Now)
}

type spaceRPC struct {
	scanner             *bufio.Scanner
	writer              *json.Encoder
	id                  int
	readTool, writeTool string
	failed              bool
}

func newSpaceRPC(r io.Reader, w io.Writer) *spaceRPC {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 2<<20)
	return &spaceRPC{scanner: s, writer: json.NewEncoder(w)}
}

func (rpc *spaceRPC) call(method string, params any, result any) (err error) {
	if rpc.failed {
		return fmt.Errorf("Space bridge stopped after an earlier protocol failure")
	}
	defer func() {
		if err != nil {
			rpc.failed = true
		}
	}()
	switch method {
	case "initialize", "thread/resume", "mcpServerStatus/list", "mcpServer/tool/call":
	default:
		return fmt.Errorf("unsupported Space bridge method")
	}
	rpc.id++
	if err := rpc.writer.Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.id, "method": method, "params": params}); err != nil {
		return err
	}
	for notifications := 0; notifications < 1000 && rpc.scanner.Scan(); notifications++ {
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(rpc.scanner.Bytes(), &msg); err != nil {
			return fmt.Errorf("invalid app-server response")
		}
		if msg.Method != "" {
			if msg.ID != nil {
				return fmt.Errorf("Space bridge refused an app-server request; the operator must resolve approval or authentication")
			}
			continue
		}
		if msg.ID == nil || *msg.ID != rpc.id {
			return fmt.Errorf("unexpected app-server response ID")
		}
		if len(msg.Error) > 0 {
			return fmt.Errorf("app-server rejected %s; publication has not been verified", method)
		}
		if len(msg.Result) == 0 {
			return fmt.Errorf("app-server returned no result")
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
	return fmt.Errorf("app-server ended without a matching response")
}

func (rpc *spaceRPC) connect(target SpaceTarget) error {
	if err := rpc.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "plan-dash", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true, "explicitGatewayOauth": true}}, nil); err != nil {
		return err
	}
	if err := rpc.writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return err
	}
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := rpc.call("thread/resume", map[string]any{"threadId": target.ThreadID, "excludeTurns": true}, &resumed); err != nil {
		return err
	}
	if resumed.Thread.ID != target.ThreadID {
		return fmt.Errorf("app-server resumed a different thread")
	}
	var inventory struct {
		Data []struct {
			Name          string  `json:"name"`
			AuthStatus    string  `json:"authStatus"`
			RuntimeStatus string  `json:"runtimeStatus"`
			ToolsError    *string `json:"toolsError"`
			Tools         map[string]struct {
				Name        string          `json:"name"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := rpc.call("mcpServerStatus/list", map[string]any{"threadId": target.ThreadID, "serverName": "codex_apps", "detail": "toolsAndAuthOnly"}, &inventory); err != nil {
		return err
	}
	if inventory.NextCursor != nil || len(inventory.Data) != 1 || inventory.Data[0].Name != "codex_apps" {
		return fmt.Errorf("Pages tool inventory is incomplete")
	}
	s := inventory.Data[0]
	if s.ToolsError != nil || s.AuthStatus == "notLoggedIn" || s.RuntimeStatus != "connected" {
		return fmt.Errorf("Pages bridge is not connected; operator authentication or plugin setup is required")
	}
	for _, tool := range s.Tools {
		if !json.Valid(tool.InputSchema) {
			continue
		}
		switch SpaceOperation(tool.Name) {
		case "read":
			if rpc.readTool != "" {
				return fmt.Errorf("ambiguous Pages read tool")
			}
			rpc.readTool = tool.Name
		case "visualization":
			if rpc.writeTool != "" {
				return fmt.Errorf("ambiguous Pages visualization tool")
			}
			rpc.writeTool = tool.Name
		}
	}
	if rpc.readTool == "" || rpc.writeTool == "" {
		return fmt.Errorf("Pages snapshot tools are unavailable")
	}
	return nil
}

func (rpc *spaceRPC) tool(target SpaceTarget, name string, args any, result any) error {
	if name != rpc.readTool && name != rpc.writeTool || name == "" {
		return fmt.Errorf("unreviewed Space bridge tool")
	}
	var envelope struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := rpc.call("mcpServer/tool/call", map[string]any{"threadId": target.ThreadID, "server": "codex_apps", "tool": name, "arguments": args}, &envelope); err != nil {
		return err
	}
	if len(envelope.StructuredContent) == 0 {
		return fmt.Errorf("Pages returned no structured content")
	}
	if err := json.Unmarshal(envelope.StructuredContent, result); err != nil {
		return fmt.Errorf("invalid Pages response")
	}
	if envelope.IsError {
		return fmt.Errorf("Pages rejected the request; inspect Page state before another write")
	}
	return nil
}

type spaceBlock struct {
	ID       string `json:"id"`
	Hash     string `json:"hash"`
	Kind     string `json:"kind"`
	Markdown string `json:"markdown"`
}

func (rpc *spaceRPC) read(target SpaceTarget) (spaceBlock, *int64, error) {
	var p struct {
		Content *struct {
			PageID   string       `json:"page_id"`
			Blocks   []spaceBlock `json:"blocks"`
			Sequence *int64       `json:"through_sequence"`
		} `json:"content"`
		Metadata struct {
			PageID     string `json:"page_id"`
			StreamKind string `json:"stream_kind"`
		} `json:"metadata"`
		Selection *struct {
			Complete bool `json:"selection_complete"`
		} `json:"selection"`
		Error json.RawMessage `json:"error"`
	}
	err := rpc.tool(target, rpc.readTool, map[string]any{"page_id": target.PageID, "block_ids": []string{target.BlockID}}, &p)
	if err != nil {
		return spaceBlock{}, nil, err
	}
	if len(p.Error) > 0 || p.Content == nil || p.Content.PageID != target.PageID || p.Metadata.PageID != target.PageID ||
		(p.Metadata.StreamKind != "" && p.Metadata.StreamKind != "content") || p.Selection == nil || !p.Selection.Complete ||
		(p.Content.Sequence != nil && *p.Content.Sequence < 0) {
		return spaceBlock{}, nil, fmt.Errorf("Pages did not return the complete configured target")
	}
	var block spaceBlock
	for _, b := range p.Content.Blocks {
		if b.ID != target.BlockID {
			continue
		}
		if block.ID != "" || b.Hash == "" || b.Kind != "markdown" || !visualizationMarkdown(b.Markdown) {
			return spaceBlock{}, nil, fmt.Errorf("configured block is not a single owned visualization")
		}
		block = b
	}
	if block.ID == "" {
		return block, nil, fmt.Errorf("configured visualization block was not found")
	}
	return block, p.Content.Sequence, nil
}

func visualizationMarkdown(s string) bool {
	return strings.HasPrefix(s, "![Agent plans](visualize:") && strings.HasSuffix(s, ")") &&
		spaceID.MatchString(strings.TrimSuffix(strings.TrimPrefix(s, "![Agent plans](visualize:"), ")"))
}

func publishSpace(target SpaceTarget, html []byte, rpc *spaceRPC, stateFile string, now func() time.Time) (SpaceReceipt, error) {
	var empty SpaceReceipt
	state, err := loadSpaceState(stateFile)
	if err != nil {
		return empty, err
	}
	if err := rpc.connect(target); err != nil {
		return empty, err
	}
	block, sequence, err := rpc.read(target)
	if err != nil {
		return empty, err
	}
	hash := PageHash(html)
	if state.Pending != nil {
		p := state.Pending
		if p.PageID != target.PageID || p.BlockID != target.BlockID || p.Markdown == "" || p.Markdown != block.Markdown {
			return empty, fmt.Errorf("previous publication outcome is unresolved after readback; operator reconciliation is required before another upload")
		}
		p.BlockHash, p.VerifiedAt = block.Hash, now().UTC()
		state.Published, state.Pending = p, nil
		if err := saveSpaceState(stateFile, state); err != nil {
			return empty, err
		}
	}
	if p := state.Published; p != nil && p.PageID == target.PageID && p.BlockID == target.BlockID && p.SnapshotHash == hash && p.Markdown == block.Markdown && p.BlockHash == block.Hash {
		return *p, nil
	}
	args := SpaceVisualization{PageID: target.PageID, BaseSequence: sequence, Title: SpaceTitle, HTML: string(html)}
	args.ReplaceBlock.BlockID, args.ReplaceBlock.ExpectedHash = target.BlockID, block.Hash
	input, err := json.Marshal(args)
	if err != nil {
		return empty, err
	}
	if err := AuthorizeSpaceSnapshot(input); err != nil {
		return empty, err
	}
	state.Pending = &SpaceReceipt{PageID: target.PageID, BlockID: target.BlockID, SnapshotHash: hash, BlockHash: block.Hash}
	if err := saveSpaceState(stateFile, state); err != nil {
		return empty, err
	}
	var response struct {
		PageID        string          `json:"page_id"`
		Markdown      string          `json:"markdown"`
		FileReference string          `json:"file_reference"`
		Error         json.RawMessage `json:"error"`
		Edit          struct {
			Error      json.RawMessage `json:"error"`
			Operations []struct {
				Index    int      `json:"operation_index"`
				Status   string   `json:"status"`
				Ignored  *string  `json:"ignored_reason"`
				Error    *string  `json:"error_code"`
				Created  []string `json:"created_block_ids"`
				Affected []struct {
					ID   string `json:"block_id"`
					Hash string `json:"hash"`
				} `json:"affected_blocks"`
			} `json:"operation_results"`
		} `json:"edit"`
	}
	callErr := rpc.tool(target, rpc.writeTool, args, &response)
	if response.PageID == target.PageID && visualizationMarkdown(response.Markdown) && response.Markdown == "![Agent plans](visualize:"+response.FileReference+")" {
		state.Pending.Markdown = response.Markdown
		if err := saveSpaceState(stateFile, state); err != nil {
			return empty, err
		}
	}
	// Read after every attempted upload, including ambiguous errors. An upload
	// with no usable receipt remains pending: never create another file blindly.
	readback, _, readErr := rpc.read(target)
	if callErr != nil || len(response.Error) > 0 || len(response.Edit.Error) > 0 || len(response.Edit.Operations) != 1 {
		return empty, fmt.Errorf("Space publication receipt was not successful; outcome retained for reconciliation")
	}
	op := response.Edit.Operations[0]
	if op.Index != 0 || (op.Status != "" && op.Status != "applied") || op.Ignored != nil || op.Error != nil || len(op.Created) != 0 || len(op.Affected) != 1 || op.Affected[0].ID != target.BlockID || op.Affected[0].Hash == "" {
		return empty, fmt.Errorf("Space receipt did not confirm replacement of the exact owned block; operator reconciliation is required")
	}
	if readErr != nil || state.Pending.Markdown == "" || readback.Markdown != state.Pending.Markdown || readback.Hash != op.Affected[0].Hash {
		return empty, fmt.Errorf("Space publication readback did not match its receipt")
	}
	state.Pending.BlockHash, state.Pending.VerifiedAt = readback.Hash, now().UTC()
	state.Published, state.Pending = state.Pending, nil
	if err := saveSpaceState(stateFile, state); err != nil {
		return empty, err
	}
	return *state.Published, nil
}

func saveSpaceState(file string, state spaceState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".publication-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), file); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
