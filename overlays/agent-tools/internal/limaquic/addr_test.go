package limaquic

import (
	"net/netip"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true},
		{"2606:4700::1111", true},
		{"0.1.2.3", false},         // this network
		{"10.1.2.3", false},        // private
		{"100.64.0.1", false},      // CGNAT
		{"100.63.255.255", true},   // just below CGNAT
		{"127.0.0.1", false},       // loopback
		{"169.254.1.1", false},     // link-local
		{"172.16.0.1", false},      // private
		{"172.32.0.1", true},       // just above 172.16/12
		{"192.0.0.9", false},       // IETF protocol assignments
		{"192.168.5.2", false},     // private, Lima's host address
		{"198.18.0.1", false},      // benchmarking
		{"198.20.0.1", true},       // just above 198.18/15
		{"224.0.0.1", false},       // multicast
		{"240.0.0.1", false},       // reserved
		{"255.255.255.255", false}, // broadcast
		{"::", false},              // unspecified
		{"::1", false},             // loopback
		{"::10.0.0.1", false},      // IPv4-compatible
		{"::ffff:10.0.0.1", false}, // IPv4-mapped private
		{"::ffff:8.8.8.8", true},   // IPv4-mapped public
		{"64:ff9b::a00:1", false},  // NAT64
		{"64:ff9b:1::1", false},    // local-use NAT64
		{"2001::1", false},         // Teredo
		{"2002:a00:1::1", false},   // 6to4
		{"fc00::1", false},         // unique local
		{"fe80::1", false},         // link-local
		{"ff02::1", false},         // multicast
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := publicAddr(netip.MustParseAddr(tc.addr)); got != tc.public {
				t.Fatalf("publicAddr(%s) = %v, want %v", tc.addr, got, tc.public)
			}
		})
	}
}

func TestUpstreamCheck(t *testing.T) {
	// A public address standing in for one of the host's own, as a host
	// with a public interface has.
	own := netip.MustParseAddr("203.0.113.9")
	allowed := upstreamCheck(map[netip.Addr]bool{own: true})
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"203.0.113.10", true},
		{"203.0.113.9", false},        // the host's own
		{"::ffff:203.0.113.9", false}, // the host's own, IPv4-mapped
		{"10.0.0.1", false},           // not public
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := allowed(netip.MustParseAddr(tc.addr)); got != tc.ok {
				t.Fatalf("allowed(%s) = %v, want %v", tc.addr, got, tc.ok)
			}
		})
	}
}

func TestLocalAddrs(t *testing.T) {
	local, err := localAddrs()
	if err != nil {
		t.Fatal(err)
	}
	if !local[netip.MustParseAddr("127.0.0.1")] {
		t.Fatalf("the host's addresses %v lack its loopback address", local)
	}
}
