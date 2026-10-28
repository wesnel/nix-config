package review

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
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	shared "github.com/wesnel/agent-tools/internal/availability"
)

// This subprocess replaces account/network access. It accepts only the fixed
// initialization and read sequence; all model, tool and login RPCs fail.
func allowanceTestServer() int {
	if len(os.Args) > 1 && os.Args[1] == "allowance-child" {
		ready := os.NewFile(3, "ready")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		allowanceTestHang()
		return 0
	}
	dir, err := os.Getwd()
	if err != nil {
		return 2
	}
	start := struct {
		Args []string
		Dir  string
	}{os.Args[1:], dir}
	if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "allowance-start.json"), must(json.Marshal(start)), 0o600); err != nil {
		return 2
	}
	scan := bufio.NewScanner(os.Stdin)
	for i, expected := range []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"git-delegate-allowance","version":"1"},"capabilities":{"explicitGatewayOauth":true}}}`,
		`{"method":"initialized"}`,
		`{"id":2,"method":"account/rateLimits/read","params":{"excludeResetCreditDetails":true,"supportsLunaReserve":false}}`,
	} {
		if !scan.Scan() {
			return 2
		}
		var got, want any
		if json.Unmarshal(scan.Bytes(), &got) != nil || json.Unmarshal([]byte(expected), &want) != nil || !reflect.DeepEqual(got, want) {
			_ = os.WriteFile(filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "allowance-protocol-error"), []byte("unexpected request"), 0o600)
			return 2
		}
		if i == 0 {
			fmt.Fprintln(os.Stdout, `{"id":1,"result":{"userAgent":"fixture"}}`)
		} else if i == 2 {
			mode := os.Getenv("FAKE_ALLOWANCE")
			value := `"ordinaryUsageAllowed":null,`
			switch mode {
			case "false", "true":
				value = `"ordinaryUsageAllowed":` + mode + `,`
			case "missing":
				value = ""
			case "not-bool":
				value = `"ordinaryUsageAllowed":"false",`
			case "duplicate":
				value = `"ordinaryUsageAllowed":true,"ordinaryUsageAllowed":false,`
			case "wrong-id":
				fmt.Fprintln(os.Stdout, `{"id":9,"result":{"rateLimits":{},"ordinaryUsageAllowed":false}}`)
				return 0
			case "request":
				fmt.Fprintln(os.Stdout, `{"id":3,"method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`)
				value = `"ordinaryUsageAllowed":false,`
			case "bad-json":
				fmt.Fprintln(os.Stdout, "{")
				return 0
			case "error":
				fmt.Fprintln(os.Stdout, `{"id":2,"error":{"code":-32601,"message":"fixture-private-account"}}`)
				return 0
			case "oversized":
				fmt.Fprintf(os.Stdout, `{"method":"account/updated","params":{"data":%q}}`+"\n", strings.Repeat("a", 64<<10))
				value = `"ordinaryUsageAllowed":false,`
			case "notifications", "total":
				n, payload := 33, ""
				if mode == "total" {
					n, payload = 6, strings.Repeat("a", 60<<10)
				}
				for range n {
					fmt.Fprintf(os.Stdout, `{"method":"account/updated","params":{"data":%q}}`+"\n", payload)
				}
				value = `"ordinaryUsageAllowed":false,`
			case "stderr":
				fmt.Fprint(os.Stderr, strings.Repeat("x", 300<<10))
				value = `"ordinaryUsageAllowed":false,`
			case "hang":
				_ = os.WriteFile(filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "allowance-group"), []byte(strconv.Itoa(os.Getpid())), 0o600)
				ready := filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "allowance-ready")
				if _, err := os.Stat(ready); err == nil {
					_ = os.WriteFile(ready, []byte{1}, 0o600)
				}
				allowanceTestHang()
			case "descendant":
				reader, writer, err := os.Pipe()
				if err != nil {
					return 2
				}
				child := exec.Command(os.Args[0], "allowance-child")
				child.ExtraFiles = []*os.File{writer}
				child.Stdout, child.Stderr = os.Stdout, os.Stderr
				if child.Start() != nil {
					return 2
				}
				_ = writer.Close()
				_, err = io.ReadFull(reader, make([]byte, 1))
				_ = reader.Close()
				if err != nil {
					return 2
				}
				_ = os.WriteFile(filepath.Join(os.Getenv("FAKE_CODEX_LOG"), "allowance-group"), []byte(strconv.Itoa(os.Getpid())), 0o600)
				value = `"ordinaryUsageAllowed":false,`
			case "disable-true", "disable-false", "disable-claude":
				p := shared.Codex
				if mode == "disable-claude" {
					p = shared.Claude
					mode = "disable-true"
				}
				if shared.Hold(p, shared.State{Reason: shared.Disabled}) != nil {
					return 2
				}
				value = `"ordinaryUsageAllowed":` + strings.TrimPrefix(mode, "disable-") + `,`
			}
			fmt.Fprintf(os.Stdout, `{"id":2,"result":{"rateLimits":{},%s"accountId":"fixture-private-account"}}`+"\n", value)
		}
	}
	return 0
}

func allowanceTestHang() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	<-stop
}

func allowanceCalls(t *testing.T, f *fixture) []string {
	t.Helper()
	return strings.Fields(string(must(os.ReadFile(filepath.Join(f.log, "calls")))))
}

func allowanceReview(t *testing.T, ctx context.Context, f *fixture) (int, string) {
	t.Helper()
	var errs bytes.Buffer
	code := Main(ctx, []string{"--reviewer", "codex", "--goal", ".git/goal.md", f.base + "..HEAD"}, strings.NewReader(""), io.Discard, &errs)
	return code, errs.String()
}

func TestAllowanceAdmission(t *testing.T) {
	t.Run("false prevents model launch", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "none")
		t.Setenv("FAKE_ALLOWANCE", "false")
		code, _, errs := runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(errs, "primary harness") || strings.Contains(errs, "fixture-private-account") {
			t.Fatalf("allowance refusal: exit %d, %s", code, errs)
		}
		if calls := string(must(os.ReadFile(filepath.Join(f.log, "calls")))); calls != "app-server\n" {
			t.Fatalf("exhaustion launched another operation: %q", calls)
		}
		if data := string(must(os.ReadFile(shared.File(shared.Codex)))); data != `{"reason":"quota"}` {
			t.Fatalf("persisted more than the allowance decision: %s", data)
		}
	})
	t.Run("ask shares one query", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_CODEX", "answer")
		t.Setenv("FAKE_ALLOWANCE", "true")
		if code, _, errs := askCodex(t, "--question", ".git/question.md", "code.txt"); code != 0 {
			t.Fatalf("ask: %d %s", code, errs)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server", "login", "sandbox", "exec"}) {
			t.Fatalf("ask launched %v", got)
		}
	})
	t.Run("retry shares one query", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "bad-reply")
		if code, _, _ := runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
			t.Fatalf("invalid response exit %d", code)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server", "login", "sandbox", "exec", "sandbox", "exec"}) {
			t.Fatalf("retry launched %v", got)
		}
	})
	t.Run("direct runner admits before touching checkout", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "false")
		_, err := CodexRun(t.Context(), Codex{Checkout: t.TempDir(), Timeout: time.Minute, Log: io.Discard})
		var unavailable providerUnavailable
		if !errors.As(err, &unavailable) {
			t.Fatalf("direct runner error: %v", err)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server"}) {
			t.Fatalf("direct runner launched %v", got)
		}
	})
	t.Run("failed persistence blocks later roles", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "false")
		if err := os.MkdirAll(shared.File(shared.Codex)+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, errs := runDelegate(t, "--reviewer", "codex", "--reviewer", "diagram", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(errs, "could not remember") {
			t.Fatalf("failed hold: %d %s", code, errs)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server"}) {
			t.Fatalf("failed persistence allowed later launches: %v", got)
		}
		noProviderCall(t, f, "diagrammer-runs")
	})
	for _, mode := range []string{"disable-true", "disable-false"} {
		t.Run(mode, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_ALLOWANCE", mode)
			if code, _, errs := runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
				t.Fatalf("concurrent disable: %d %s", code, errs)
			}
			if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server"}) {
				t.Fatalf("concurrent disable allowed launch: %v", got)
			}
			data := string(must(os.ReadFile(shared.File(shared.Codex))))
			if data != `{"reason":"disabled"}` {
				t.Fatalf("probe replaced disable: %s", data)
			}
		})
	}
	t.Run("diagram denial precedes drawing", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "false")
		if code, _, errs := runDelegate(t, "--reviewer", "diagram", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
			t.Fatalf("diagram admission: %d %s", code, errs)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server"}) {
			t.Fatalf("diagram launched %v", got)
		}
		noProviderCall(t, f, "diagrammer-runs")
	})
	t.Run("diagram rechecks both providers after probing", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "disable-claude")
		if code, _, errs := runDelegate(t, "--reviewer", "diagram", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
			t.Fatalf("diagram admission: %d %s", code, errs)
		}
		if got := allowanceCalls(t, f); !reflect.DeepEqual(got, []string{"app-server"}) {
			t.Fatalf("Claude disable during probe allowed a later process: %v", got)
		}
		noProviderCall(t, f, "diagrammer-runs")
	})
	t.Run("local status clear and prompt start nothing", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_ALLOWANCE", "false")
		for _, args := range [][]string{{"availability"}, {"availability", "--clear", "codex"}, {"ask", "--print-prompt", "--question", ".git/question.md", "code.txt"}, {"--print-prompt", "--reviewer", "codex", "--goal", ".git/goal.md", f.base + "..HEAD"}} {
			if code, _, errs := delegateCmd(t, args...); code != 0 {
				t.Fatalf("local command: %d %s", code, errs)
			}
		}
		noProviderCall(t, f, "calls", "adversarial-reviewer-runs")
	})
	for _, policy := range []string{"PR_LOOP_SESSION", "CODEX_THREAD_ID"} {
		t.Run("local policy "+policy, func(t *testing.T) {
			f := askFixture(t, true)
			t.Setenv("FAKE_CODEX", "answer")
			t.Setenv(policy, "fixture")
			if code, _, errs := askCodex(t, "--reviewer", "codex", "--question", ".git/question.md", "code.txt"); code != 4 {
				t.Fatalf("policy: %d %s", code, errs)
			}
			noProviderCall(t, f, "calls")
		})
	}
}

func TestAllowanceProtocol(t *testing.T) {
	for _, mode := range []string{"false", "true", "null", "missing", "not-bool", "duplicate", "wrong-id", "request", "bad-json", "error", "oversized", "notifications", "total", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_ALLOWANCE", mode)
			t.Setenv("FAKE_CODEX", "none")
			wantCode := 0
			if mode == "false" {
				wantCode = 4
			}
			if code, errs := allowanceReview(t, t.Context(), f); code != wantCode || strings.Contains(errs, "fixture-private-account") {
				t.Fatalf("mode %q: exit %d, want %d: %s", mode, code, wantCode, errs)
			}
			if mode != "false" {
				if _, err := os.Stat(shared.File(shared.Codex)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unknown/allowed result changed availability state: %v", err)
				}
			}
			var start struct {
				Args []string
				Dir  string
			}
			if err := json.Unmarshal(must(os.ReadFile(filepath.Join(f.log, "allowance-start.json"))), &start); err != nil {
				t.Fatal(err)
			}
			sqlite := filepath.Join(os.TempDir(), filepath.Base(start.Dir), "sqlite")
			want := []string{"app-server", "--listen", "stdio://", "--strict-config", "--disable", "plugins", "--disable", "apps", "--disable", "hooks", "--disable", "background_paginated_rollout_migration", "--disable", "local_thread_store_compression", "-c", "project_root_markers=[]", "-c", `model_provider="openai"`, "-c", "sqlite_home=" + strconv.Quote(sqlite), "-c", "analytics.enabled=false", "-c", `otel.exporter="none"`, "-c", `otel.trace_exporter="none"`, "-c", `otel.metrics_exporter="none"`}
			if !reflect.DeepEqual(start.Args, want) || filepath.Dir(start.Dir) != must(filepath.EvalSymlinks(os.TempDir())) || !strings.HasPrefix(filepath.Base(start.Dir), "delegate-allowance-") {
				t.Fatalf("startup: %+v", start)
			}
			if _, err := os.Stat(start.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("allowance left its cwd/sqlite behind: %v", err)
			}
			noProviderCall(t, f, "allowance-protocol-error")
		})
	}
	t.Run("cancellation kills and reaps", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "hang")
		path := filepath.Join(f.log, "allowance-ready")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		ready, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ready.Close() })
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		signaled := make(chan error, 1)
		go func() {
			_, err := io.ReadFull(ready, make([]byte, 1))
			signaled <- err
			cancel()
		}()
		start := time.Now()
		if code, errs := allowanceReview(t, ctx, f); code != 4 {
			t.Fatalf("canceled admission: %d %s", code, errs)
		}
		if _, err := os.Stat(shared.File(shared.Codex)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancellation recorded exhaustion: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("deadline cleanup took %s", elapsed)
		}
		select {
		case err := <-signaled:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("preflight never reached the cancellation signal")
		}
		allowanceGroupGone(t, f)
	})
	t.Run("own deadline bounds an unresponsive server", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "hang")
		t.Setenv("FAKE_CODEX", "none")
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		start := time.Now()
		if code, errs := allowanceReview(t, ctx, f); code != 0 {
			t.Errorf("bounded unknown must leave time for the review: %d %s", code, errs)
		}
		if elapsed := time.Since(start); elapsed > 7*time.Second {
			t.Fatalf("own deadline failed: %s", elapsed)
		}
	})
	t.Run("success kills descendants", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_ALLOWANCE", "descendant")
		if code, errs := allowanceReview(t, t.Context(), f); code != 4 {
			t.Fatalf("lost completed denial: %d %s", code, errs)
		}
		allowanceGroupGone(t, f)
	})
}

func allowanceGroupGone(t *testing.T, f *fixture) {
	t.Helper()
	pgid, err := strconv.Atoi(string(must(os.ReadFile(filepath.Join(f.log, "allowance-group")))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("preflight left descendants: %v", err)
	}
}
