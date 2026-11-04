package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"time"
)

var instanceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > 1024*1024-b.buffer.Len() {
		return 0, fmt.Errorf("output exceeds limit")
	}
	return b.buffer.Write(p)
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }

func call(ctx context.Context, executable string, params json.RawMessage, timeout time.Duration) map[string]any {
	fail := func(message string) map[string]any {
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": message}}}
	}
	var request struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return fail("invalid request")
	}
	var args []string
	switch request.Name {
	case "lima_list":
		var input struct{}
		if err := strict(request.Arguments, &input); err != nil {
			return fail("invalid arguments")
		}
		args = []string{"list", "--format", "{{.Name}}\t{{.Status}}"}
	case "lima_stop":
		var input struct {
			Instance string `json:"instance"`
		}
		if err := strict(request.Arguments, &input); err != nil || !instanceName.MatchString(input.Instance) {
			return fail("invalid arguments")
		}
		args = []string{"stop", "--", input.Instance}
	default:
		return fail("unknown tool")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + os.TempDir()}
	var output limitedBuffer
	var diagnostics limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	if err := cmd.Run(); err != nil {
		if request.Name == "lima_list" {
			return fail(fmt.Sprintf("limactl list failed: %v", err))
		}
		return fail(fmt.Sprintf("limactl failed: %v\n%s%s", err, output.String(), diagnostics.String()))
	}
	if request.Name != "lima_list" {
		if _, err := output.Write(diagnostics.Bytes()); err != nil {
			return fail(err.Error())
		}
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": output.String()}}}
}

func strict(raw json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func serve(ctx context.Context, in io.Reader, out io.Writer, executable string) error {
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(req.ID) == 0 {
			continue
		}
		if req.Method != "initialize" && req.Method != "ping" && req.Method != "tools/list" && req.Method != "tools/call" {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}}); err != nil {
				return err
			}
			continue
		}
		result := map[string]any{}
		if req.Method == "initialize" {
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			version := "2025-06-18"
			if params.ProtocolVersion == "2024-11-05" || params.ProtocolVersion == "2025-03-26" {
				version = params.ProtocolVersion
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "lima", "version": "1"}}
		}
		if req.Method == "tools/call" {
			result = call(ctx, executable, req.Params, 5*time.Minute)
		}
		if req.Method == "tools/list" {
			result["tools"] = []any{
				map[string]any{"name": "lima_list", "description": "List Lima instances and their status", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
				map[string]any{"name": "lima_stop", "description": "Stop a Lima instance", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"instance": map[string]any{"type": "string"}}, "required": []string{"instance"}, "additionalProperties": false}},
			}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			return err
		}
	}
}

func main() {
	if err := serve(context.Background(), os.Stdin, os.Stdout, "/opt/homebrew/bin/limactl"); err != nil {
		os.Exit(1)
	}
}
