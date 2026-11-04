package gitgrant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

// stateDir is a state home without symlinks on its path, which the grant
// directory requires; $TMPDIR may be one, such as /tmp on macOS.
func stateDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConsume(t *testing.T) {
	t.Run("refuses a symlinked grant", func(t *testing.T) {
		state := stateDir(t)
		hostpaths.Bake(t, "", state)
		args := []byte(`{"path":"file"}`)
		r := Record{Tool: "stage", Session: "session", Call: "call", Arguments: args}
		token, err := Issue(r)
		if err != nil {
			t.Fatal(err)
		}
		r.Expires = time.Now().Add(time.Minute)
		data, _ := json.Marshal(r)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, data, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, "agent-tools", "git-authorizations", token)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Consume(token, "stage", "session", args); err == nil {
			t.Fatal("symlinked grant was accepted")
		}
	})
	t.Run("issuing a grant removes expired grants", func(t *testing.T) {
		state := stateDir(t)
		hostpaths.Bake(t, "", state)
		r := Record{Tool: "stage", Session: "session", Call: "call", Arguments: []byte(`{}`)}
		old, err := Issue(r)
		if err != nil {
			t.Fatal(err)
		}
		r.Expires = time.Now().Add(-time.Minute)
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, "agent-tools", "git-authorizations", old)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Issue(r); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired grant remains: %v", err)
		}
	})
	t.Run("refuses an expired grant", func(t *testing.T) {
		state := stateDir(t)
		hostpaths.Bake(t, "", state)
		args := []byte(`{"path":"file"}`)
		r := Record{Tool: "stage", Session: "session", Call: "call", Arguments: args}
		token, err := Issue(r)
		if err != nil {
			t.Fatal(err)
		}
		r.Expires = time.Now().Add(-time.Minute)
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(state, "agent-tools", "git-authorizations", token)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Consume(token, "stage", "session", args); err == nil {
			t.Fatal("expired grant was accepted")
		}
	})
	t.Run("refuses a symlinked authority parent", func(t *testing.T) {
		state := stateDir(t)
		hostpaths.Bake(t, "", state)
		if err := os.Symlink(t.TempDir(), filepath.Join(state, "agent-tools")); err != nil {
			t.Fatal(err)
		}
		_, err := Issue(Record{Tool: "stage", Session: "session", Call: "call", Arguments: []byte(`{}`)})
		if err == nil {
			t.Fatal("authorization issued through a symlinked parent")
		}
	})
	t.Run("oversized authorization is refused", func(t *testing.T) {
		hostpaths.Bake(t, "", stateDir(t))
		args := []byte(`{"path":"` + strings.Repeat("x", 65536) + `"}`)
		if _, err := Issue(Record{Tool: "stage", Session: "session", Call: "call", Arguments: args}); err == nil {
			t.Fatal("oversized authorization was accepted")
		}
	})
	t.Run("only one concurrent caller can consume a grant", func(t *testing.T) {
		hostpaths.Bake(t, "", stateDir(t))
		args := []byte(`{"paths":["one"],"worktree":"/work"}`)
		token, err := Issue(Record{Tool: "stage", Session: "session", Call: "call", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var successes atomic.Int32
		var wait sync.WaitGroup
		start := make(chan struct{})
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				if _, err := Consume(token, "stage", "session", args); err == nil {
					successes.Add(1)
				}
			}()
		}
		close(start)
		wait.Wait()
		if got := successes.Load(); got != 1 {
			t.Fatalf("successful consumers = %d, want 1", got)
		}
	})
}
