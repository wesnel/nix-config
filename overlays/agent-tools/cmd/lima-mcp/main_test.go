package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func TestTools(t *testing.T) {
	var output bytes.Buffer
	err := serve(context.Background(), bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &output, "unused")
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range reply.Result.Tools {
		names = append(names, tool.Name)
	}
	if len(names) != 2 || names[0] != "lima_list" || names[1] != "lima_stop" {
		t.Fatalf("tools = %v; want list and stop", names)
	}
}

func TestUnknownMethod(t *testing.T) {
	var out bytes.Buffer
	if err := serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"missing"}`), &out, "unused"); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.Error == nil || reply.Error.Code != -32601 {
		t.Fatalf("unknown method response: %s", out.String())
	}
}

func TestInitialize(t *testing.T) {
	var out bytes.Buffer
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`
	if err := serve(context.Background(), strings.NewReader(request), &out, "unused"); err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result struct {
			Version string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.Result.Version != "2024-11-05" {
		t.Fatalf("initialize response: %s", out.String())
	}
}

func fakeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cli := filepath.Join(dir, "limactl")
	source := filepath.Join(dir, "main.go")
	const fake = `package main
import ("fmt"; "io"; "net"; "os"; "os/exec"; "strings")
func main() {
  if len(os.Args) == 3 && os.Args[1] == "child" {
    c, err := net.Dial("tcp", os.Args[2]); if err != nil { os.Exit(1) }
    fmt.Fprintln(c, "ready"); io.Copy(io.Discard, c); c.Close(); return
  }
  if len(os.Args) == 4 && strings.HasPrefix(os.Args[3], "hold-") {
    c := exec.Command(os.Args[0], "child", "127.0.0.1:"+strings.TrimPrefix(os.Args[3], "hold-"))
    c.Stdout, c.Stderr = os.Stdout, os.Stderr; _ = c.Run(); return
  }
  if len(os.Args) > 1 && os.Args[1] == "list" { fmt.Fprintln(os.Stderr, "unreviewed-detail") }
  if len(os.Args) > 3 && os.Args[3] == "env" { fmt.Println("token="+os.Getenv("MCP_PROXY_TOKEN")); return }
  if len(os.Args) > 3 && os.Args[3] == "large" { fmt.Print(strings.Repeat("x", (1<<20)+1)); return }
  if len(os.Args) > 3 && os.Args[3] == "stderr" { fmt.Println("success"); fmt.Fprintln(os.Stderr, "diagnostic"); return }
  if len(os.Args) > 3 && os.Args[3] == "fail" { fmt.Fprintln(os.Stderr, "failure"); os.Exit(7) }
  for _, arg := range os.Args[1:] { fmt.Println(arg) }
}`
	if err := os.WriteFile(source, []byte(fake), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", cli, source)
	cmd.Env = append(os.Environ(), "GO111MODULE=off", "GOWORK=off", "GOPROXY=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake CLI: %v\n%s", err, output)
	}
	return cli
}

func TestCalls(t *testing.T) {
	// A fake CLI avoids touching the operator's VMs.
	t.Setenv("MCP_PROXY_TOKEN", "fixture-token")
	cli := fakeCLI(t)
	direct := exec.Command(cli, "stop", "--", "env")
	direct.Env = os.Environ()
	output, err := direct.Output()
	if err != nil || !strings.Contains(string(output), "token=fixture-token") {
		t.Fatalf("fake CLI did not observe inherited fixture: %q, %v", output, err)
	}
	for _, tc := range []struct {
		name, request, want string
		fail                bool
	}{
		{"list", `{"name":"lima_list","arguments":{}}`, "list\n--format\n{{.Name}}\t{{.Status}}", false},
		{"exec refused", `{"name":"lima_exec","arguments":{"instance":"vm-1","command":["printf","a b"]}}`, "unknown tool", true},
		{"stop", `{"name":"lima_stop","arguments":{"instance":"vm-1"}}`, "stop\n--\nvm-1", false},
		{"environment", `{"name":"lima_stop","arguments":{"instance":"env"}}`, "token=\n", false},
		{"large output", `{"name":"lima_stop","arguments":{"instance":"large"}}`, "output exceeds limit", true},
		{"stderr", `{"name":"lima_stop","arguments":{"instance":"stderr"}}`, "success\ndiagnostic", false},
		{"failure", `{"name":"lima_stop","arguments":{"instance":"fail"}}`, "failure", true},
		{"option name", `{"name":"lima_stop","arguments":{"instance":"--all"}}`, "", true},
		{"unknown", `{"name":"lima_delete","arguments":{"instance":"vm-1"}}`, "", true},
		{"extra argument", `{"name":"lima_stop","arguments":{"instance":"vm-1","force":true}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + tc.request + `}`
			if err := serve(context.Background(), strings.NewReader(request), &out, cli); err != nil {
				t.Fatal(err)
			}
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Result.IsError != tc.fail || len(reply.Result.Content) == 0 || !strings.Contains(reply.Result.Content[0].Text, tc.want) {
				t.Fatalf("unexpected reply: error=%v, content count=%d, expected text found=%v", reply.Result.IsError, len(reply.Result.Content), len(reply.Result.Content) > 0 && strings.Contains(reply.Result.Content[0].Text, tc.want))
			}
			if tc.name == "list" && strings.Contains(reply.Result.Content[0].Text, "unreviewed-detail") {
				t.Fatalf("list exposed diagnostics: %s", out.String())
			}
		})
	}
}

func TestCancelClosesPipes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cli := fakeCLI(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	params, err := json.Marshal(map[string]any{"name": "lima_stop", "arguments": map[string]any{
		"instance": fmt.Sprintf("hold-%d", listener.Addr().(*net.TCPAddr).Port),
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() { done <- call(ctx, cli, params, 5*time.Minute) }()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ready [6]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil || string(ready[:]) != "ready\n" {
		t.Fatalf("child readiness: %q, %v", ready, err)
	}
	cancel()
	select {
	case result := <-done:
		if result["isError"] != true {
			t.Fatalf("canceled call succeeded: %v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled call waited for a descendant holding its output pipes")
	}
}

func TestDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cli := fakeCLI(t)
	params, err := json.Marshal(map[string]any{"name": "lima_stop", "arguments": map[string]any{
		"instance": fmt.Sprintf("hold-%d", listener.Addr().(*net.TCPAddr).Port),
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]any, 1)
	go func() { done <- call(context.Background(), cli, params, 2*time.Second) }()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ready [6]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil || string(ready[:]) != "ready\n" {
		t.Fatalf("child readiness: %q, %v", ready, err)
	}
	select {
	case result := <-done:
		if result["isError"] != true {
			t.Fatalf("timed-out call succeeded: %v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not stop waiting at its deadline")
	}
}
