package signpass

import (
	"strconv"
	"strings"
	"testing"
)

// A brief rebase reports each 25th signature once it is done, and the last,
// and keeps what git says besides its progress for a failure.
func TestBriefWriter(t *testing.T) {
	var out strings.Builder
	w := &briefStderr{label: "one", out: &out}
	for i := 1; i <= 60; i++ {
		w.Write([]byte("Rebasing (" + strconv.Itoa(i) + "/60)\r"))
	}
	w.Write([]byte("Successfully rebased and updated refs/heads/one.\n"))
	w.finish(true)
	if got, want := out.String(), "signing one: 25/60\nsigning one: 50/60\nsigning one: 60/60\n"; got != want {
		t.Errorf("progress %q, want %q", got, want)
	}
	if got := w.kept.String(); got != "Successfully rebased and updated refs/heads/one.\n" {
		t.Errorf("kept %q", got)
	}

	// A failure after step 3 has signed 2 commits and reports neither as the last.
	out.Reset()
	w = &briefStderr{label: "one", out: &out}
	w.Write([]byte("Rebasing (1/3)\rRebasing (2/3)\rRebasing (3/3)\rerror: could not sign\nhint: x\n"))
	w.finish(false)
	if out.String() != "" || w.kept.String() != "error: could not sign\nhint: x\n" {
		t.Errorf("progress %q, kept %q", out.String(), w.kept.String())
	}
	if !strings.Contains(w.all.String(), "Rebasing (3/3)") {
		t.Errorf("the whole of stderr is %q", w.all.String())
	}
}
