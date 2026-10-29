package codexrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// reply is the result of the operator's own account/rateLimits/read, with
// the account's IDs removed.
const operatorReply = `{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,` +
	`"primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":1793268465},` +
	`"secondary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1793855265},` +
	`"credits":{"hasCredits":true,"unlimited":false,"balance":null},"individualLimit":null,"spendControlReached":false,` +
	`"planType":"team","rateLimitReachedType":null},"rateLimitResetCredits":{"availableCount":0,"credits":[]},"rateLimitUpsell":null}`

func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_CODEX") {
	case "":
	case "detached-child":
		os.WriteFile(os.Getenv("FAKE_CODEX_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		hang()
	default:
		os.Exit(fakeCodex(os.Getenv("FAKE_CODEX")))
	}
	gitenv.Isolate()
	os.Exit(m.Run())
}

// fakeCodex stands in for `codex app-server`, which would reach the
// operator's OpenAI account. It answers only the probe's exact requests,
// and writes what it was started with to FAKE_CODEX_LOG.
func fakeCodex(mode string) int {
	log := os.Getenv("FAKE_CODEX_LOG")
	os.WriteFile(filepath.Join(log, "args"), []byte(strings.Join(os.Args[1:], " ")), 0o600)
	scan := bufio.NewScanner(os.Stdin)
	for i, expected := range []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"agent-plan-codex-usage","version":"1"}}}`,
		`{"method":"initialized"}`,
		`{"id":2,"method":"account/rateLimits/read","params":{}}`,
	} {
		if !scan.Scan() {
			return 2
		}
		var got, want any
		if json.Unmarshal(scan.Bytes(), &got) != nil || json.Unmarshal([]byte(expected), &want) != nil || !reflect.DeepEqual(got, want) {
			os.WriteFile(filepath.Join(log, "unexpected"), scan.Bytes(), 0o600)
			return 2
		}
		switch i {
		case 0:
			fmt.Println(`{"id":1,"result":{"userAgent":"fixture"}}`)
		case 2:
			return answer(mode)
		}
	}
	return 0
}

func answer(mode string) int {
	switch mode {
	case "answer":
		fmt.Println(`{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`)
	case "error":
		fmt.Println(`{"error":{"code":-32600,"message":"codex account authentication required to read rate limits"},"id":2}`)
		return 0
	case "wrong-id":
		fmt.Printf(`{"id":9,"result":%s}`+"\n", operatorReply)
		return 0
	case "bad-json":
		fmt.Println("{")
		return 0
	case "request":
		fmt.Println(`{"id":3,"method":"account/chatgptAuthTokens/refresh","params":{}}`)
	case "no-rate-limits":
		fmt.Println(`{"id":2,"result":{"ordinaryUsageAllowed":true}}`)
		return 0
	case "oversized":
		fmt.Printf(`{"method":"account/updated","params":{"data":%q}}`+"\n", strings.Repeat("a", 64<<10))
	case "notifications":
		for range 33 {
			fmt.Println(`{"method":"account/updated","params":{}}`)
		}
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 300<<10))
	case "exits":
		return 1
	case "detached":
		os.WriteFile(filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "group"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "FAKE_CODEX=detached-child")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout = os.Stdout
		if child.Start() != nil {
			return 2
		}
		hang()
	}
	fmt.Printf(`{"id":2,"result":%s}`+"\n", operatorReply)
	return 0
}

func hang() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	<-stop
	os.Exit(0)
}

// fake makes the test binary the codex Read starts, in mode.
func fake(t *testing.T, mode string) (codex, log string) {
	t.Helper()
	log = t.TempDir()
	t.Setenv("FAKE_CODEX", mode)
	t.Setenv("FAKE_CODEX_LOG", log)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe, log
}

// Read sends the request the operator's probe did, and nothing that
// overrides Codex's own configuration, and gives back the call's result.
func TestRead(t *testing.T) {
	codex, log := fake(t, "answer")
	got, err := Read(t.Context(), codex)
	if err != nil {
		data, _ := os.ReadFile(filepath.Join(log, "unexpected"))
		t.Fatalf("Read: %v (unexpected request: %s)", err, data)
	}
	if string(got) != operatorReply {
		t.Errorf("result %s, want %s", got, operatorReply)
	}
	if args, _ := os.ReadFile(filepath.Join(log, "args")); string(args) != "app-server --listen stdio://" {
		t.Errorf("started with %q", args)
	}
}

// A reply Read cannot trust as the call's result is an error, never a
// result.
func TestReadRefuses(t *testing.T) {
	for _, mode := range []string{"error", "wrong-id", "bad-json", "request", "no-rate-limits", "oversized", "notifications", "stderr", "exits"} {
		t.Run(mode, func(t *testing.T) {
			codex, _ := fake(t, mode)
			if got, err := Read(t.Context(), codex); err == nil {
				t.Errorf("result %s from %s", got, mode)
			}
		})
	}
}

// Cancelling Read ends it even while a process Codex detached holds its
// output open, and leaves nothing of Codex's process group running.
func TestReadCancelled(t *testing.T) {
	codex, log := fake(t, "detached")
	// The detached child writes its pid here once all three requests are
	// read; reading the fifo waits for it.
	pidFile := filepath.Join(log, "pid")
	if err := syscall.Mkfifo(pidFile, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CODEX_PID", pidFile)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := Read(ctx, codex)
		done <- err
	}()
	child := make(chan int, 1)
	go func() {
		data, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(string(data))
		child <- pid
	}()
	select {
	case pid := <-child:
		if pid <= 0 {
			t.Fatal("the detached child did not say who it is")
		}
		t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	case <-time.After(5 * time.Second):
		t.Fatal("the detached child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a result after cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Read still running 3s after it was cancelled")
	}
	data, err := os.ReadFile(filepath.Join(log, "group"))
	if err != nil {
		t.Fatal(err)
	}
	pgid, _ := strconv.Atoi(string(data))
	t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Codex's process group is still there: %v", err)
	}
}
