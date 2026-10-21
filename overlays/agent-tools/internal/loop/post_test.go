package loop

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/record"
)

// fakeTTY stands in for /dev/tty: it reads the operator's answers and keeps
// what was shown.
type fakeTTY struct {
	io.Reader
	shown bytes.Buffer
}

func (f *fakeTTY) Write(p []byte) (int, error) { return f.shown.Write(p) }
func (f *fakeTTY) Close() error                { return nil }

// useTTY has Post read answers from a fake terminal.
func useTTY(t *testing.T, answers string) *fakeTTY {
	t.Helper()
	tty := &fakeTTY{Reader: strings.NewReader(answers)}
	old := openTTY
	openTTY = func() (io.ReadWriteCloser, error) { return tty, nil }
	t.Cleanup(func() { openTTY = old })
	return tty
}

// draftedRepo is fakeRepo in a people-mode loop with PR 2's record written,
// a draft for its person's thread, human2, and the bot's, bot2.
func draftedRepo(t *testing.T) (*fakeGitHub, *[]string) {
	t.Helper()
	f := fakeRepo(t)
	t.Setenv(ModeEnv, "people")
	if _, err := Threads(2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Draft("human2", "Fixed in \"Check the input\".", "fix"); err != nil {
		t.Fatal(err)
	}
	if _, err := Draft("bot2", "Not a problem: the input is checked above.", "rebut"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ModeEnv, "")
	var requested []string
	old := requestReviewer
	requestReviewer = func(slug string, number int, reviewer string) error {
		requested = append(requested, reviewer)
		return nil
	}
	t.Cleanup(func() { requestReviewer = old })
	return f, &requested
}

func TestDraft(t *testing.T) {
	t.Run("people mode only", func(t *testing.T) {
		for _, mode := range []string{"", "bots"} {
			fakeRepo(t)
			t.Setenv(ModeEnv, mode)
			if _, err := Draft("human2", "x", "fix"); !refusedWell(err) {
				t.Errorf("mode %q: %v", mode, err)
			}
		}
	})

	t.Run("written to the record", func(t *testing.T) {
		f, _ := draftedRepo(t)
		path, _ := record.PRFile("o", "r", 2)
		p, err := record.ReadPR(path)
		if err != nil {
			t.Fatal(err)
		}
		if d := p.Threads[2].Draft; p.Threads[2].ID != "human2" || d == nil || d.Outcome != "fix" || !d.Posted.IsZero() {
			t.Errorf("record %+v", p.Threads)
		}
		if len(f.replies) != 0 {
			t.Errorf("a draft was posted: %v", f.replies)
		}
	})

	for name, args := range map[string][3]string{
		"an unknown outcome":     {"human2", "x", "propose"},
		"an empty draft":         {"human2", " ", "fix"},
		"a secret":               {"human2", "token ghp_" + strings.Repeat("a", 36), "fix"},
		"someone else's PR":      {"theirs", "x", "fix"},
		"a thread not in record": {"human1", "x", "fix"},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			fakeRepo(t)
			t.Setenv(ModeEnv, "people")
			if _, err := Draft(args[0], args[1], args[2]); err == nil {
				t.Errorf("%s was drafted", name)
			}
		})
	}

	// The record keeps a draft cleaned, and post sends what it keeps, so a
	// draft cleaning would change is refused rather than changed.
	for name, body := range map[string]string{
		"a control character": "Fixed.\r\n",
		"file variables":      "See -*- mode: org -*- here.",
		"over the cap":        strings.Repeat("a", record.TextCap+1),
	} {
		t.Run("changed by the record/"+name, func(t *testing.T) {
			draftedRepo(t)
			t.Setenv(ModeEnv, "people")
			_, err := Draft("human2", body, "fix")
			if !refusedWell(err) || !strings.Contains(err.Error(), "other than as written") {
				t.Errorf("%s: %v", name, err)
			}
			path, _ := record.PRFile("o", "r", 2)
			p, err := record.ReadPR(path)
			if err != nil {
				t.Fatal(err)
			}
			if d := p.Threads[2].Draft; d == nil || d.Body != "Fixed in \"Check the input\"." {
				t.Errorf("the draft became %+v", d)
			}
		})
	}
}

func TestPost(t *testing.T) {
	// Each draft is shown, and posted as shown once the operator says y;
	// fix and rebut resolve the thread; once the person's thread has its
	// answer, they are asked to review again.
	t.Run("confirmed", func(t *testing.T) {
		f, requested := draftedRepo(t)
		tty := useTTY(t, "y\ny\n")
		got, err := Post(2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if f.replies["human2"] != "Fixed in \"Check the input\"." || f.replies["bot2"] != "Not a problem: the input is checked above." {
			t.Errorf("replies %v", f.replies)
		}
		if !strings.Contains(tty.shown.String(), "Fixed in \"Check the input\".\n=== Post this reply? [y/N] ") {
			t.Errorf("shown:\n%s", tty.shown.String())
		}
		if fmt.Sprint(f.resolved) != "[bot2 human2]" || fmt.Sprint(*requested) != "[josh]" {
			t.Errorf("resolved %v, requested %v, got %v", f.resolved, *requested, got)
		}
		path, _ := record.PRFile("o", "r", 2)
		p, _ := record.ReadPR(path)
		if p.Threads[2].Draft.Posted.IsZero() || p.Threads[0].Draft.Posted.IsZero() {
			t.Errorf("not marked posted: %+v", p.Threads)
		}
		// Posted drafts are not offered again.
		useTTY(t, "y\n")
		if _, err := Post(2, nil); err != nil || len(f.replies) != 2 {
			t.Errorf("posting again: %v, %v", err, f.replies)
		}
	})

	t.Run("n skips", func(t *testing.T) {
		f, requested := draftedRepo(t)
		useTTY(t, "n\ny\n")
		if _, err := Post(2, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.replies["bot2"]; ok || f.replies["human2"] == "" {
			t.Errorf("replies %v", f.replies)
		}
		if len(*requested) != 1 {
			t.Errorf("requested %v", *requested)
		}
	})

	t.Run("named threads alone", func(t *testing.T) {
		f, requested := draftedRepo(t)
		useTTY(t, "y\n")
		if _, err := Post(2, []string{"bot2"}); err != nil {
			t.Fatal(err)
		}
		if len(f.replies) != 1 || f.replies["bot2"] == "" || len(*requested) != 0 {
			t.Errorf("replies %v, requested %v", f.replies, *requested)
		}
	})

	t.Run("no terminal", func(t *testing.T) {
		f, _ := draftedRepo(t)
		old := openTTY
		openTTY = func() (io.ReadWriteCloser, error) { return nil, errors.New("device not configured") }
		t.Cleanup(func() { openTTY = old })
		if _, err := Post(2, nil); !refusedWell(err) || len(f.replies) != 0 {
			t.Errorf("no terminal: %v, %v", err, f.replies)
		}
	})
}
