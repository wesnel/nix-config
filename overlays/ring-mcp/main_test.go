package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, repo, askScript string, timeout time.Duration) *httptest.Server {
	t.Helper()
	at := time.Date(2026, 10, 13, 17, 30, 0, 0, time.UTC)
	c := &capturer{repo: repo, inbox: "inbox.org", now: func() time.Time { return at }}
	a := &asker{command: askScript, timeout: timeout}
	s := &server{token: []byte("secret"), tools: []tool{
		{Name: "capture", InputSchema: textSchema("text", ""), arg: "text", run: c.capture},
		{Name: "ask", InputSchema: textSchema("question", ""), arg: "question", run: a.ask},
	}}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, ts *httptest.Server, auth, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func call(t *testing.T, ts *httptest.Server, name, arg, value string) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]string{arg: value}})
	_, out := post(t, ts, "Bearer secret", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+string(params)+`}`)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, result["isError"].(bool)
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ask")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRefusesWithoutTheToken(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), "/bin/false", time.Second)
	for _, auth := range []string{"", "Bearer wrong", "secret", "Basic secret"} {
		resp, _ := post(t, ts, auth, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status %d", auth, resp.StatusCode)
		}
	}
}

func TestInitialize(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), "/bin/false", time.Second)
	for client, want := range map[string]string{
		"2025-06-18": "2025-06-18",
		"2025-03-26": "2025-03-26",
		"2099-01-01": protocolVersion,
	} {
		resp, out := post(t, ts, "Bearer secret",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+client+`"}}`)
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
		result := out["result"].(map[string]any)
		if got := result["protocolVersion"]; got != want {
			t.Errorf("client %s: got version %v, want %s", client, got, want)
		}
		if result["instructions"] == "" {
			t.Error("no instructions")
		}
	}
}

func TestNotificationsAreAccepted(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), "/bin/false", time.Second)
	resp, _ := post(t, ts, "Bearer secret", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status %d", resp.StatusCode)
	}
}

func TestToolsListHasNoCursor(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), "/bin/false", time.Second)
	_, out := post(t, ts, "Bearer secret", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result := out["result"].(map[string]any)
	if _, ok := result["nextCursor"]; ok {
		t.Error("nextCursor present")
	}
	if n := len(result["tools"].([]any)); n != 2 {
		t.Errorf("%d tools", n)
	}
}

func TestCaptureCommitsAnOrgEntry(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "org")
	ts := newTestServer(t, repo, "/bin/false", time.Second)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")

	for _, said := range []string{"buy milk", "* call\n  the bank"} {
		if text, isErr := call(t, ts, "capture", "text", said); isErr {
			t.Fatalf("capture failed: %s", text)
		}
	}

	got, err := os.ReadFile(filepath.Join(repo, "inbox.org"))
	if err != nil {
		t.Fatal(err)
	}
	want := "* TODO buy milk\n[2026-10-13 Tue 17:30]\n* TODO * call the bank\n[2026-10-13 Tue 17:30]\n"
	if string(got) != want {
		t.Errorf("inbox:\n%s\nwant:\n%s", got, want)
	}
	log, err := exec.Command("git", "-C", repo, "log", "--format=%s %G?").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(log) != "Capture from the ring N\nCapture from the ring N\n" {
		t.Errorf("log:\n%s", log)
	}
	status, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if len(status) != 0 {
		t.Errorf("uncommitted: %s", status)
	}
}

func TestAskPassesTheQuestionOnStdin(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), script(t, `printf 'you asked: '; cat`), time.Second)
	text, isErr := call(t, ts, "ask", "question", "is CI green?")
	if isErr || text != "you asked: is CI green?" {
		t.Errorf("got %q (error %v)", text, isErr)
	}
}

func TestAskFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		timeout time.Duration
		want    string
	}{
		"timeout": {"sleep 30", 500 * time.Millisecond, "no answer within"},
		"failure": {"echo broken >&2; exit 1", 10 * time.Second, "broken"},
		"empty":   {"true", 10 * time.Second, "empty"},
	} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, t.TempDir(), script(t, tc.body), tc.timeout)
			text, isErr := call(t, ts, "ask", "question", "anything")
			if !isErr || !strings.Contains(text, tc.want) {
				t.Errorf("got %q (error %v)", text, isErr)
			}
		})
	}
}

func TestUnknownToolAndMethod(t *testing.T) {
	ts := newTestServer(t, t.TempDir(), "/bin/false", time.Second)
	_, out := post(t, ts, "Bearer secret", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm","arguments":{}}}`)
	if out["error"] == nil {
		t.Error("unknown tool accepted")
	}
	_, out = post(t, ts, "Bearer secret", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if code := out["error"].(map[string]any)["code"]; code != float64(-32601) {
		t.Errorf("code %v", code)
	}
}
