package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// approval is how the guard treats a tool: a read it lets through, or a
// write the operator confirms. internal/guard/mcp.json lists each tool under
// its class, and TestToolTable holds that list to this table.
type approval int

const (
	read approval = iota
	confirm
)

// tool is one MCP tool: what tools/list says of it, and the function that
// answers its calls with their arguments.
type tool struct {
	name, description string
	approval          approval
	schema            map[string]any
	call              func(ctx context.Context, s *server, args json.RawMessage) map[string]any
}

// tools is every tool the server has, the reads before the confirmed
// writes, each in mcp.json's order, since mcp-proxy's allow list is
// mcp.json's reads then its confirms.
var tools = []tool{
	{
		name:        "lima_list",
		description: "List the instances of the default Lima home and their status",
		approval:    read,
		schema:      object(nil),
		call:        callList,
	},
	{
		name:        "lima_stop",
		description: "Stop an instance of the default Lima home",
		approval:    confirm,
		schema:      object(map[string]any{"instance": map[string]any{"type": "string"}}, "instance"),
		call:        callStop,
	},
}

// object is the JSON schema of an object with exactly these properties, of
// which those named are required.
func object(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// server holds what the tools need.
type server struct {
	// limactl is the Lima of the default home, Homebrew's, for lima_list
	// and lima_stop.
	limactl string
	// timeout bounds each synchronous command.
	timeout time.Duration
}

// call answers a tools/call request's params.
func (s *server) call(ctx context.Context, params json.RawMessage) map[string]any {
	var request struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return failure("invalid request")
	}
	if len(request.Arguments) == 0 {
		request.Arguments = json.RawMessage("{}")
	}
	for _, t := range tools {
		if t.name == request.Name {
			return t.call(ctx, s, request.Arguments)
		}
	}
	return failure("unknown tool")
}

func failure(message string) map[string]any {
	return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": message}}}
}

func text(message string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": message}}}
}

var instanceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func callList(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	var input struct{}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments")
	}
	output, _, err := s.defaultLima(ctx, "list", "--format", "{{.Name}}\t{{.Status}}")
	if err != nil {
		return failure(fmt.Sprintf("limactl list failed: %v", err))
	}
	return text(output)
}

func callStop(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	var input struct {
		Instance string `json:"instance"`
	}
	if err := strict(raw, &input); err != nil || !instanceName.MatchString(input.Instance) {
		return failure("invalid arguments")
	}
	output, diagnostics, err := s.defaultLima(ctx, "stop", "--", input.Instance)
	if err != nil {
		return failure(fmt.Sprintf("limactl failed: %v\n%s%s", err, output, diagnostics))
	}
	if len(output)+len(diagnostics) > maxOutput {
		return failure("output exceeds limit")
	}
	return text(output + diagnostics)
}

// defaultLima runs Homebrew's limactl against the default home, with only
// HOME, PATH and TMPDIR of lima-mcp's environment, under s.timeout.
func (s *server) defaultLima(ctx context.Context, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.limactl, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + os.TempDir()}
	var output, diagnostics limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	err := cmd.Run()
	return output.String(), diagnostics.String(), err
}

// maxOutput caps what a call returns.
const maxOutput = 1 << 20

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > maxOutput-b.buffer.Len() {
		return 0, fmt.Errorf("output exceeds limit")
	}
	return b.buffer.Write(p)
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func strict(raw json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
