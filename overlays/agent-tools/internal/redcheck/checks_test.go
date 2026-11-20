package redcheck

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTipChecks(t *testing.T) {
	dir, tip := fixedRepo(t)
	base := gitIn(t, dir, "rev-parse", "HEAD^")
	for _, fixups := range []bool{false, true} {
		opts := Options{Dir: dir, Fixups: fixups}
		data := `{"Checks":[{"name":"fails","command":"echo changed > calc.go; echo failed; exit 7"},{"name":"isolated","command":"git diff --exit-code && test ! -e notes.txt && git rev-parse HEAD"}]}`
		if err := json.Unmarshal([]byte(data), &opts); err != nil {
			t.Fatal(err)
		}
		result, err := Do(t.Context(), base+".."+tip, opts)
		if err != nil {
			t.Fatalf("checks-only run: %v", err)
		}
		if result.Exit() != 3 {
			t.Fatalf("failed check exit = %d, want 3", result.Exit())
		}
		raw, _ := json.Marshal(result)
		var got struct {
			TipChecks struct {
				Base, Tip string
				Checks    []struct {
					Name, Command string
					Run           *Run
				}
			}
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.TipChecks.Base != base || got.TipChecks.Tip != tip || len(got.TipChecks.Checks) != 2 {
			t.Fatalf("wrong evidence: %s", raw)
		}
		checks := got.TipChecks.Checks
		if checks[0].Run == nil || checks[0].Run.Exit != 7 || checks[1].Run == nil || checks[1].Run.Exit != 0 || checks[1].Run.Output != tip {
			t.Fatalf("checks not isolated at tip or stopped after failure: %s", raw)
		}
		if got := gitIn(t, dir, "status", "--porcelain"); got != "?? notes.txt" {
			t.Fatalf("caller changed: %s", got)
		}
	}
	t.Run("cancellation", func(t *testing.T) {
		opts := Options{Dir: dir}
		json.Unmarshal([]byte(`{"Checks":[{"name":"never","command":"true"}]}`), &opts)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if result, err := Do(ctx, base+".."+tip, opts); err == nil || result != nil {
			t.Fatalf("cancelled evidence: %+v, %v", result, err)
		}
	})
	t.Run("signal", func(t *testing.T) {
		opts := Options{Dir: dir}
		json.Unmarshal([]byte(`{"Checks":[{"name":"signal","command":"kill -KILL $$"}]}`), &opts)
		if result, err := Do(t.Context(), base+".."+tip, opts); err == nil || result != nil {
			t.Fatalf("signal counted as complete: %+v, %v", result, err)
		}
	})
}

func TestRunRedactsBeforeBounds(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		lines         int
	}{
		{"private key before line cut", "printf '%s\\n' '-----BEGIN PRIVATE KEY-----' 'short-sensitive-body' '-----END PRIVATE KEY-----'", 1},
		{"secret before byte cut", "printf 'password='; awk 'BEGIN {for(i=0;i<20000;i++) printf \"x\"}'", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := run(t.Context(), t.TempDir(), tc.command, tc.lines)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Output) > 16384 || strings.Contains(r.Output, "short-sensitive-body") || strings.Contains(r.Output, strings.Repeat("x", 100)) || !strings.Contains(r.Output, "REDACTED") {
				t.Fatalf("unredacted or unbounded output: %.100s (length %d)", r.Output, len(r.Output))
			}
		})
	}
	r, err := run(t.Context(), t.TempDir(), "awk 'BEGIN {for(i=0;i<20000;i++) printf \".\"}'", 40)
	if err != nil || len(r.Output) > 16384 || len(r.Output) == 0 {
		t.Fatalf("byte cap: length %d, %v", len(r.Output), err)
	}
}
