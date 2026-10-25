package usage

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// at is a time between the fixtures' observation and their earliest reset.
var at = time.Unix(1792021200, 0)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Claude Code's status line input carries the account's windows once the
// session has had a response.
func TestFromClaudeStatus(t *testing.T) {
	t.Run("before the first response", func(t *testing.T) {
		if r, ok := FromClaudeStatus(fixture(t, "claude-statusline-first.json"), at); ok {
			t.Errorf("reading %+v from an input without rate_limits", r)
		}
	})
	t.Run("after it", func(t *testing.T) {
		r, ok := FromClaudeStatus(fixture(t, "claude-statusline.json"), at)
		want := []Window{
			{Name: "5h", Used: 100, ResetsAt: 1792032000, ObservedAt: 1792021200},
			{Name: "7d", Used: 31, ResetsAt: 1792602000, ObservedAt: 1792021200},
		}
		if !ok || !reflect.DeepEqual(r, want) {
			t.Errorf("got %+v, %v; want %+v", r, ok, want)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		if _, ok := FromClaudeStatus([]byte("{"), at); ok {
			t.Error("a reading from broken input")
		}
	})
}

// onDisk is one window of the usage file, as its JSON has it.
type onDisk struct {
	Used       float64 `json:"used_percentage"`
	ResetsAt   int64   `json:"resets_at"`
	ObservedAt int64   `json:"observed_at"`
}

func readFile(t *testing.T, file string) map[string]onDisk {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]onDisk
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%q: %v", data, err)
	}
	return got
}

// Record keeps each window's highest reading and its latest sighting until
// a later window replaces it, however sessions' status lines interleave.
func TestRecord(t *testing.T) {
	const resets = 1792032000
	// record writes a reading of the 5h window, used percent, resetting
	// at resetsAt, seen at observed.
	record := func(t *testing.T, file string, used float64, resetsAt, observed int64) {
		t.Helper()
		if err := Record(file, []Window{{Name: "5h", Used: used, ResetsAt: resetsAt, ObservedAt: observed}}); err != nil {
			t.Fatal(err)
		}
	}
	// start is a usage file holding 5h at 60% seen at 100, and 7d.
	start := func(t *testing.T) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "agent-plan", "usage.json")
		err := Record(file, []Window{
			{Name: "5h", Used: 60, ResetsAt: resets, ObservedAt: 100},
			{Name: "7d", Used: 31, ResetsAt: resets + 86400, ObservedAt: 100},
		})
		if err != nil {
			t.Fatal(err)
		}
		return file
	}

	t.Run("the first reading", func(t *testing.T) {
		got := readFile(t, start(t))
		want := map[string]onDisk{
			"five_hour": {Used: 60, ResetsAt: resets, ObservedAt: 100},
			"seven_day": {Used: 31, ResetsAt: resets + 86400, ObservedAt: 100},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
	for _, c := range []struct {
		name               string
		used               float64
		resetsAt, observed int64
		want               onDisk
	}{
		{"an equal reading refreshes when it was seen", 60, resets, 200, onDisk{60, resets, 200}},
		{"a higher reading replaces it", 75, resets, 200, onDisk{75, resets, 200}},
		// Another session's fresher, lower reading keeps the window current.
		{"a lower reading refreshes only when it was seen", 40, resets, 200, onDisk{60, resets, 200}},
		{"an older, higher reading keeps the later sighting", 75, resets, 50, onDisk{75, resets, 100}},
		{"a later window replaces it", 2, resets + 5*3600, 200, onDisk{2, resets + 5*3600, 200}},
		// An idle session repeats its reading of a window that has reset.
		{"an earlier window changes nothing", 99, resets - 5*3600, 200, onDisk{60, resets, 100}},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := start(t)
			record(t, file, c.used, c.resetsAt, c.observed)
			got := readFile(t, file)
			if got["five_hour"] != c.want {
				t.Errorf("five_hour: got %+v, want %+v", got["five_hour"], c.want)
			}
			if want := (onDisk{31, resets + 86400, 100}); got["seven_day"] != want {
				t.Errorf("seven_day, not in the reading: got %+v, want %+v", got["seven_day"], want)
			}
		})
	}

	// A reader holding the file open while Record writes reads the whole
	// version it opened, never the new one half written: Record replaces
	// the file rather than writing into it.
	t.Run("a reader keeps the version it opened", func(t *testing.T) {
		file := start(t)
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		record(t, file, 75, resets, 200)
		got, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(before) {
			t.Errorf("the open file reads %q, not the version it opened, %q", got, before)
		}
		if now := readFile(t, file)["five_hour"]; now != (onDisk{75, resets, 200}) {
			t.Errorf("five_hour after the write: %+v", now)
		}
	})
}

// Sessions recording at once, some with an earlier window than others,
// leave the latest window stored: no Record writes over another's newer
// window with what it loaded before that one was written.
func TestRecordConcurrent(t *testing.T) {
	const resets, writers, rounds = 1792032000, 8, 50
	for round := range rounds {
		file := filepath.Join(t.TempDir(), "usage.json")
		var wg sync.WaitGroup
		for i := range writers {
			// Earlier and later windows interleave: 0, 7, 1, 6, ...
			k := i / 2
			if i%2 == 1 {
				k = writers - 1 - i/2
			}
			wg.Go(func() {
				if err := Record(file, []Window{{Name: "5h", Used: 50, ResetsAt: resets + int64(k)*3600, ObservedAt: 100}}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got, want := readFile(t, file)["five_hour"].ResetsAt, int64(resets+(writers-1)*3600); got != want {
			t.Fatalf("round %d: stored resets_at %d, want the latest, %d", round, got, want)
		}
	}
}
