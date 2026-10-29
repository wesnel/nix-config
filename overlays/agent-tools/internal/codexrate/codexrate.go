// Package codexrate reads the Codex account's rate limits through Codex's
// app-server, as the account's own Codex sees them: it runs `codex
// app-server` with the user's configuration and login, and sends only
// initialize and account/rateLimits/read, so no model, tool, approval or
// login call is made.
package codexrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// Timeout bounds a whole Read: starting Codex, its login and the call to
// OpenAI.
const Timeout = 30 * time.Second

// The bounds on what Codex may send: one frame, everything on stdout and
// stderr together, and notifications before the reply.
const (
	maxFrame         = 64 << 10
	maxOutput        = 256 << 10
	maxNotifications = 32
)

// ErrProtocol is a reply Read cannot take as the call's result: a JSON-RPC
// error, a request from the server, a frame that is not one JSON object or
// is over the bounds, or a result without rateLimits.
var ErrProtocol = errors.New("codex app-server: no usable account/rateLimits/read reply")

// Read runs codex, the path of the Codex executable, as `codex app-server
// --listen stdio://`, calls account/rateLimits/read, and returns the call's
// result, a JSON object holding rateLimits. It sends the requests as a
// plain client does, overriding none of Codex's configuration, since an
// override such as a model provider or --strict-config makes Codex refuse
// the call or a configuration it would otherwise load. It kills Codex's
// process group before it returns, within Timeout or once ctx ends.
func Read(parent context.Context, codex string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, codex, "app-server", "--listen", "stdio://")
	cmd.Env = gitenv.Without(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	cmd.Cancel = func() error {
		// A process Codex detached can hold stdout open after its group
		// is gone, so the scan below wakes only once stdout is closed.
		_ = stdout.Close()
		stopGroup(cmd.Process.Pid, time.Second)
		return nil
	}
	budget := &outputBudget{cancel: cancel}
	cmd.Stderr = budget
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		stopGroup(cmd.Process.Pid, time.Second)
		<-done
	}()
	scan := bufio.NewScanner(io.TeeReader(stdout, budget))
	scan.Buffer(make([]byte, 4096), maxFrame)
	notifications := 0
	if _, err := io.WriteString(stdin, `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"agent-plan-codex-usage","version":"1"}}}`+"\n"); err != nil {
		return nil, err
	}
	if _, err := reply(scan, 1, &notifications); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if _, err := io.WriteString(stdin, `{"method":"initialized"}`+"\n"+
		`{"id":2,"method":"account/rateLimits/read","params":{}}`+"\n"); err != nil {
		return nil, err
	}
	result, err := reply(scan, 2, &notifications)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("account/rateLimits/read: %w", err)
	}
	fields, err := object(result)
	if err != nil {
		return nil, err
	}
	if _, err := object(fields["rateLimits"]); err != nil {
		return nil, err
	}
	return result, nil
}

// stopGroup kills the process group pgid until it is empty, or for at most
// bound. One kill is not enough: a process that forks as the kill lands can
// leave a child in the group that the kill missed.
func stopGroup(pgid int, bound time.Duration) {
	deadline := time.Now().Add(bound)
	for syscall.Kill(-pgid, syscall.SIGKILL) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// outputBudget counts what Codex writes to stdout and stderr, and ends the
// Read once it passes maxOutput.
type outputBudget struct {
	bytes  atomic.Int64
	cancel context.CancelFunc
}

func (b *outputBudget) Write(p []byte) (int, error) {
	if b.bytes.Add(int64(len(p))) > maxOutput {
		b.cancel()
		return 0, ErrProtocol
	}
	return len(p), nil
}

// reply is the result of the response to request id, skipping at most
// maxNotifications notifications in all.
func reply(scan *bufio.Scanner, id int, notifications *int) (json.RawMessage, error) {
	for scan.Scan() {
		frame, err := object(scan.Bytes())
		if err != nil {
			return nil, err
		}
		if version, ok := frame["jsonrpc"]; ok && string(version) != `"2.0"` {
			return nil, ErrProtocol
		}
		if method, ok := frame["method"]; ok {
			var name string
			if _, request := frame["id"]; request || json.Unmarshal(method, &name) != nil || name == "" || frame["result"] != nil || frame["error"] != nil {
				return nil, ErrProtocol
			}
			*notifications++
			if *notifications > maxNotifications {
				return nil, ErrProtocol
			}
			continue
		}
		if string(frame["id"]) != strconv.Itoa(id) || frame["error"] != nil || frame["result"] == nil {
			return nil, ErrProtocol
		}
		return frame["result"], nil
	}
	return nil, ErrProtocol
}

// object is data's fields, if data is one JSON object with no field twice:
// with a field twice, a decoder would keep whichever it read last.
func object(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, ErrProtocol
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil, ErrProtocol
		}
		fields[key] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, ErrProtocol
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrProtocol
	}
	return fields, nil
}
