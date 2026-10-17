package loop

import (
	"strings"
	"testing"

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
		if _, err := Reply("human1", asPosted); !leaked(err, "tZ1Cm") {
			t.Errorf("reply naming a short key: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted: %v", f.replies)
		}
	})

	t.Run("the short key of a thread on the PR", func(t *testing.T) {
		f := fakeRepo(t)
		f.threads["PRRT_kwDOKj0Wfs5aB3dE"] = &fakeThread{pr: 1, head: "feat", author: "me", state: "OPEN", starter: "josh", last: "josh"}
		if _, err := Reply("human1", "Same cause as aB3dE: fixed in 1a2b3c4."); !leaked(err, "aB3dE") {
			t.Errorf("reply naming another thread's key: %v", err)
		}
		if _, err := Reply("human1", "Same cause as https://github.com/o/r/pull/1#discussion_r2: fixed in 1a2b3c4."); err != nil {
			t.Errorf("reply linking the thread: %v", err)
		}
	})

	t.Run("a file of replies", func(t *testing.T) {
		f := fakeRepo(t)
		_, err := ReplyAll([]PlannedReply{{Thread: "human1", Body: "Fixed in 1a2b3c4."}, {Thread: "human2", Body: "Deferred per D188."}})
		if !leaked(err, "D188") || !strings.Contains(err.Error(), "human2") {
			t.Errorf("reply file with a plan ID: %v", err)
		}
		if len(f.replies) != 0 {
			t.Errorf("posted: %v", f.replies)
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
