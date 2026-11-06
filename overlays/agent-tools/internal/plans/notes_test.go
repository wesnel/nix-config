package plans

import (
	"testing"
	"time"
)

func TestDigestName(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		if got := DigestFile(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)); got != "digests/2026/11-06-demo.org" {
			t.Errorf("DigestFile = %q", got)
		}
	})
	t.Run("day", func(t *testing.T) {
		day, ok := DigestDay(2026, "11-06-demo.org", time.UTC)
		if !ok || !day.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("DigestDay = %v, %v", day, ok)
		}
	})
	t.Run("not a day", func(t *testing.T) {
		for _, name := range []string{"02-30-demo.org", "13-01-demo.org", "11-06-demo.html", "11-06-brag.org", "1-06-demo.org"} {
			if _, ok := DigestDay(2026, name, time.UTC); ok {
				t.Errorf("DigestDay(%q) is a digest", name)
			}
		}
	})
}
