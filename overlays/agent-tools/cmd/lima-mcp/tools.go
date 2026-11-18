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
		name: "lima_dev_status",
		description: "Report the managed VM's state: absent, stopped, running-unattested, attested or mismatch. " +
			"Attesting checks the running QEMU against the launch guard; a VM that fails is stopped. " +
			"The call fails, leaving the VM as it is, when what attestation reads cannot be read.",
		approval: read,
		schema:   object(nil),
		call:     callStatus,
	},
	{
		name:        "lima_dev_job_result",
		description: "Wait up to wait_seconds (at most 240) for a managed VM job and return its status, exit code and output (its last MiB)",
		approval:    read,
		schema: object(map[string]any{
			"job":          map[string]any{"type": "string"},
			"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": maxWaitSeconds},
		}, "job"),
		call: callJobResult,
	},
	{
		name:        "lima_dev_job_cancel",
		description: "Cancel a managed VM job, killing its command",
		approval:    read,
		schema:      object(map[string]any{"job": map[string]any{"type": "string"}}, "job"),
		call:        callJobCancel,
	},
	{
		name:        "lima_stop",
		description: "Stop an instance of the default Lima home",
		approval:    confirm,
		schema:      object(map[string]any{"instance": map[string]any{"type": "string"}}, "instance"),
		call:        callStop,
	},
	{
		name: "lima_dev_start",
		description: "Start the managed VM as a job, creating it from its template first if it is absent, " +
			"and attest it. Returns {job}; read it with lima_dev_job_result.",
		approval: confirm,
		schema:   object(nil),
		call:     callStart,
	},
	{
		name:        "lima_dev_stop",
		description: "Stop the managed VM",
		approval:    confirm,
		schema:      object(nil),
		call:        callDevStop,
	},
	{
		name: "lima_dev_exec_start",
		description: "Run a bash script in the managed VM, in its configured working directory, as a job, once the VM attests. " +
			"The guest kills it after timeout_seconds (default 600, at most 7200). Returns {job}; read it with lima_dev_job_result.",
		approval: confirm,
		schema: object(map[string]any{
			"script":          map[string]any{"type": "string"},
			"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": maxExecSeconds},
		}, "script"),
		call: callExecStart,
	},
	{
		name: "lima_dev_import",
		description: "Import a repository into the managed VM's configured working directory as a job. First run `lima-dev-export NAME` in the repository, " +
			"which writes NAME.bundle (HEAD, origin/main, the index and the worktree, untracked files included but the ignored) into the outbox. " +
			"The import refuses a bundle whose history holds a credential's path, then makes the guest's branch work, index, worktree and untracked files the repository's. " +
			"Changes made only in the guest are discarded: its edits, staged or not, its untracked files and its commits on work; only its ignored files stay. " +
			"It fails while another import or a lima_dev_exec_start command runs. Returns {job}; read it with lima_dev_job_result.",
		approval: confirm,
		schema: object(map[string]any{
			"name": map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,63}$"},
		}, "name"),
		call: callImport,
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
	// managed is the managed VM, nil unless lima-mcp runs with --config.
	managed *managed
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
