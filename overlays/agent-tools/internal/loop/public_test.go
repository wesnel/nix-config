package loop

import (
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// leaked reports whether err refuses text for naming token, saying what to
// do instead.
func leaked(err error, token string) bool {
	return refused(err) && strings.Contains(err.Error(), `"`+token+`"`) && refusal.SaysInstead(err.Error())
}

func TestReplyPublic(t *testing.T) {
	asPosted := "This came from the fix for the symlinked `~/.local` case (tZ1Cm), which predates this PR."

	t.Run("a thread's short key", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		if _, err := Reply("bot1", asPosted); !leaked(err, "tZ1Cm") {
			t.Errorf("reply naming a short key: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted: %v", f.replies)
		}
	})

	t.Run("the short key of a thread on the PR", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		f.threads["PRRT_kwDOKj0Wfs5aB3dE"] = &fakeThread{pr: 1, author: "me", state: "OPEN", path: "a.go", comments: []fakeComment{josh}}
		if _, err := Reply("bot1", "Same cause as aB3dE: fixed in 1a2b3c4."); !leaked(err, "aB3dE") {
			t.Errorf("reply naming another thread's key: %v", err)
		}
		if _, err := Reply("bot1", "Same cause as https://github.com/o/r/pull/1#discussion_r2: fixed in 1a2b3c4."); err != nil {
			t.Errorf("reply linking the thread: %v", err)
		}
	})

	t.Run("a file of replies", func(t *testing.T) {
		f := fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		_, err := ReplyAll([]PlannedReply{{Thread: "bot1", Body: "Fixed in 1a2b3c4."}, {Thread: "bot2", Body: "Deferred per D188."}})
		if !leaked(err, "D188") || !strings.Contains(err.Error(), "bot2") {
			t.Errorf("reply file with a plan ID: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted: %v", f.replies)
		}
	})
}

// A draft is posted as written, so one that leaks never reaches the
// record, and the draft already there stays.
func TestDraftPublic(t *testing.T) {
	for name, c := range map[string]struct{ body, token string }{
		"a thread's short key":       {"This came from the fix for the symlinked case (tZ1Cm).", "tZ1Cm"},
		"another thread's short key": {"Same cause as aB3dE: fixed in 1a2b3c4.", "aB3dE"},
		"a plan ID":                  {"Deferred per D188.", "D188"},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := draftedRepo(t)
			f.threads["PRRT_kwDOKj0Wfs5aB3dE"] = &fakeThread{pr: 2, author: "me", state: "OPEN", path: "d.go", comments: []fakeComment{josh}}
			t.Setenv(ModeEnv, "people")
			if _, err := Draft("human2", c.body, "fix"); !leaked(err, c.token) {
				t.Errorf("draft naming %q: %v", c.token, err)
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
	t.Run("a draft linking the thread", func(t *testing.T) {
		draftedRepo(t)
		t.Setenv(ModeEnv, "people")
		if _, err := Draft("human2", "Same cause as https://github.com/o/r/pull/2#discussion_r2: fixed in 1a2b3c4.", "fix"); err != nil {
			t.Errorf("draft linking the thread: %v", err)
		}
	})
}

func TestOpenPublic(t *testing.T) {
	fakeRepo(t)
	for _, c := range []struct{ title, body, token string }{
		{"Resolve symlinks first", "Fixes the symlinked case (tZ1Cm).", "tZ1Cm"},
		{"Resolve symlinks first (D188)", "Fixes the symlinked case.", "D188"},
		{"Resolve symlinks first", "Drafted in /tmp/claude-501/s/pr.md.", "/tmp/claude-501/s/pr.md"},
	} {
		if _, err := Open("feat", "", c.title, c.body, true); !leaked(err, c.token) {
			t.Errorf("open with %q: %v", c.token, err)
		}
	}
}
