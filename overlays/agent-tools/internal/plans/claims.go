package plans

import (
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// ClaimsFocus is the focus of the route receipt a claims audit logs
// (`git fresh-review audit`). It is no route: no routed event names it, so
// RouteState never owes it, and its receipt's Findings are the claims it
// left unsupported, never findings to settle.
const ClaimsFocus = "claims"

// ClaimsAudited reports whether this host's plans hold an audit of the
// claims whose claims.Hash is hash, for the change at tip in the
// repository at top, with every claim supported: the latest claims receipt
// of that hash whose range, read again at tip, still has the patch ID it
// recorded, so that a rewrite keeping the change keeps it and any other
// change does not. Where that receipt left claims unsupported, it returns
// them.
func (c Config) ClaimsAudited(top, tip, hash string) (bool, []string, error) {
	own, err := c.ownPlans()
	if err != nil {
		return false, nil, err
	}
	var latest *Event
	var at time.Time
	ids := map[string]string{}
	for _, p := range own {
		events, err := p.Events()
		if err != nil {
			continue
		}
		for i := range events {
			e := &events[i]
			given, _, ok := strings.Cut(e.Range, "..")
			if e.Kind != routeReceiptKind || e.Focus != ClaimsFocus || e.Claims != hash || !ok || e.PatchID == "" {
				continue
			}
			base, err := git.Run("-C", top, "rev-parse", "--verify", "-q", "--end-of-options", given+"^{commit}")
			if err != nil {
				continue
			}
			id, seen := ids[base]
			if !seen {
				id, _ = git.PatchID([]string{"-C", top}, base, tip)
				ids[base] = id
			}
			if id != e.PatchID {
				continue
			}
			when, _ := time.ParseInLocation("[2006-01-02 Mon 15:04]", e.Time, time.Local)
			if latest == nil || !when.Before(at) {
				latest, at = e, when
			}
		}
	}
	if latest == nil {
		return false, nil, nil
	}
	unsupported := strings.Fields(latest.Findings)
	return len(unsupported) == 0, unsupported, nil
}
