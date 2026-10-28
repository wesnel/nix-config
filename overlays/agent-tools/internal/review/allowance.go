package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func codexExecutable() (string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// codexAllowanceDenied consumes only an authoritative false. Startup still
// loads user/managed configuration and account caches; no model, login, tool
// or approval RPC is sent. All acquisition failures leave allowance unknown.
func codexAllowanceDenied(parent context.Context) bool {
	codex, err := codexExecutable()
	if err != nil {
		return false
	}
	dir, err := os.MkdirTemp("", "delegate-allowance-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	args := []string{"app-server", "--listen", "stdio://", "--strict-config"}
	for _, feature := range []string{"plugins", "apps", "hooks", "background_paginated_rollout_migration", "local_thread_store_compression"} {
		args = append(args, "--disable", feature)
	}
	for _, setting := range []string{"project_root_markers=[]", `model_provider="openai"`,
		"sqlite_home=" + strconv.Quote(filepath.Join(dir, "sqlite")), "analytics.enabled=false",
		`otel.exporter="none"`, `otel.trace_exporter="none"`, `otel.metrics_exporter="none"`} {
		args = append(args, "-c", setting)
	}
	cmd := exec.CommandContext(ctx, codex, args...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "PATH="+realDirs(os.Getenv("PATH")))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { stopGroup(cmd.Process.Pid, time.Second); return nil }
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	defer stdout.Close()
	budget := &allowanceBudget{cancel: cancel}
	cmd.Stderr = budget
	if err := cmd.Start(); err != nil {
		return false
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		stopGroup(cmd.Process.Pid, time.Second)
		<-done
	}()
	scan := bufio.NewScanner(io.TeeReader(stdout, budget))
	scan.Buffer(make([]byte, 4096), 64<<10)
	notifications := 0
	if _, err := io.WriteString(stdin, `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"git-delegate-allowance","version":"1"},"capabilities":{"explicitGatewayOauth":true}}}`+"\n"); err != nil {
		return false
	}
	if _, err := allowanceReply(scan, 1, &notifications); err != nil {
		return false
	}
	if _, err := io.WriteString(stdin, `{"method":"initialized"}`+"\n"+
		`{"id":2,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true,"supportsLunaReserve":false}}`+"\n"); err != nil {
		return false
	}
	result, err := allowanceReply(scan, 2, &notifications)
	if err != nil || ctx.Err() != nil {
		return false
	}
	if _, err := allowanceObject(result["rateLimits"]); err != nil {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(result["ordinaryUsageAllowed"]), []byte("false"))
}

var errAllowanceProtocol = errors.New("invalid allowance reply")

type allowanceBudget struct {
	bytes  atomic.Int64
	cancel context.CancelFunc
}

func (b *allowanceBudget) Write(p []byte) (int, error) {
	if b.bytes.Add(int64(len(p))) > 256<<10 {
		b.cancel()
		return 0, errAllowanceProtocol
	}
	return len(p), nil
}

func allowanceReply(scan *bufio.Scanner, id int, notifications *int) (map[string]json.RawMessage, error) {
	for scan.Scan() {
		frame, err := allowanceObject(scan.Bytes())
		if err != nil {
			return nil, err
		}
		if version, ok := frame["jsonrpc"]; ok && string(version) != `"2.0"` {
			return nil, errAllowanceProtocol
		}
		if method, ok := frame["method"]; ok {
			var name string
			if _, request := frame["id"]; request || json.Unmarshal(method, &name) != nil || name == "" || frame["result"] != nil || frame["error"] != nil {
				return nil, errAllowanceProtocol
			}
			*notifications++
			if *notifications > 32 {
				return nil, errAllowanceProtocol
			}
			continue
		}
		if string(frame["id"]) != strconv.Itoa(id) || frame["error"] != nil {
			return nil, errAllowanceProtocol
		}
		return allowanceObject(frame["result"])
	}
	return nil, errAllowanceProtocol
}

// Duplicate fields make an authorization boolean ambiguous, including when
// a decoder would otherwise silently keep the last value.
func allowanceObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errAllowanceProtocol
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, errAllowanceProtocol
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil, errAllowanceProtocol
		}
		fields[key] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errAllowanceProtocol
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errAllowanceProtocol
	}
	return fields, nil
}
