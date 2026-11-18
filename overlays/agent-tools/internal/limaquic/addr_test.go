package limaquic

import (
	"net/netip"
	"testing"
)

// TestUpstreamCheck holds the broker to the CONNECT relay's address
// classes, which limaegress tests one by one: a sample of each kind, and
// the classes the broker once lacked.
func TestUpstreamCheck(t *testing.T) {
	// A public address standing in for one of the host's own, as a host
	// with a public interface has.
	own := netip.MustParseAddr("203.0.113.9")
	allowed := upstreamCheck([]netip.Addr{own})
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"8.8.8.8", true},
		{"203.0.113.9", false},        // the host's own
		{"::ffff:203.0.113.9", false}, // the host's own, IPv4-mapped
		{"10.0.0.1", false},           // not public
		{"fec0::1", false},            // site-local
		{"3fff::1", false},            // documentation
		{"100::1", false},             // discard-only
		{"2001:db8::1", false},        // documentation
		{"::ffff:0:a00:1", false},     // IPv4-translated
		{"4000::1", false},            // outside global unicast
		{"198.51.100.1", false},       // documentation
		{"2606:4700::1111", true},     // global unicast
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := allowed(netip.MustParseAddr(tc.addr)); got != tc.ok {
				t.Fatalf("allowed(%s) = %v, want %v", tc.addr, got, tc.ok)
			}
		})
	}
}
