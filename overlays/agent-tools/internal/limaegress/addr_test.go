package limaegress

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestServeRefusesAddress(t *testing.T) {
	for _, c := range []struct {
		name string
		addr string
	}{
		{"IPv4 loopback", "127.0.0.1"},
		{"the top of IPv4 loopback", "127.255.255.254"},
		{"IPv6 loopback", "::1"},
		{"10/8", "10.1.2.3"},
		{"the bottom of 172.16/12", "172.16.0.1"},
		{"the top of 172.16/12", "172.31.255.255"},
		{"192.168/16, the host from QEMU user networking", "192.168.5.2"},
		{"IPv4 link-local, cloud metadata", "169.254.169.254"},
		{"IPv6 link-local", "fe80::1"},
		{"the bottom of CGNAT", "100.64.0.1"},
		{"the top of CGNAT", "100.127.255.254"},
		{"ULA fc00::/8", "fc00::1"},
		{"ULA fd00::/8", "fd12:3456::1"},
		{"IPv4 multicast", "224.0.0.1"},
		{"the top of IPv4 multicast", "239.255.255.250"},
		{"IPv6 multicast", "ff02::1"},
		{"IPv4 unspecified", "0.0.0.0"},
		{"IPv6 unspecified", "::"},
		{"0.0.0.0/8", "0.1.2.3"},
		{"192.0.0.0/24", "192.0.0.170"},
		{"the bottom of 198.18/15", "198.18.0.1"},
		{"the top of 198.18/15", "198.19.255.255"},
		{"240/4", "240.0.0.1"},
		{"broadcast", "255.255.255.255"},
		{"Teredo", "2001:0:4136:e378::1"},
		{"IPv4-compatible", "::7f00:1"},
		{"NAT64", "64:ff9b::a00:1"},
		{"local-use NAT64", "64:ff9b:1::1"},
		{"6to4", "2002:c000:204::1"},
		{"IPv4-mapped loopback", "::ffff:127.0.0.1"},
		{"IPv4-mapped private", "::ffff:10.0.0.1"},
		{"a host interface's IPv4 address", "9.9.9.9"},
		{"a host interface's IPv6 address", "2620:fe::fe"},
		{"TEST-NET-1", "192.0.2.1"},
		{"TEST-NET-2", "198.51.100.1"},
		{"TEST-NET-3", "203.0.113.7"},
		{"6to4 relay anycast", "192.88.99.1"},
		{"IPv6 documentation", "2001:db8::7"},
		{"IPv6 documentation 3fff::/20", "3fff::1"},
		{"IPv6 benchmarking", "2001:2::1"},
		{"ORCHIDv2", "2001:20::1"},
		{"SRv6 SIDs", "5f00::1"},
		{"site-local", "fec0::1"},
		{"IPv4-translated", "::ffff:0:a00:1"},
		{"discard-only", "100::1"},
		{"IPv6 outside 2000::/3, below", "1000::1"},
		{"IPv6 outside 2000::/3, above", "4000::1"},
		{"IPv6 just above local-use NAT64, outside 2000::/3", "64:ff9b:2::1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.answers = []netip.Addr{netip.MustParseAddr(c.addr)}
			guest, done := f.serve()
			status, _ := connect(t, guest, "example.com:443")
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", status)
			}
			wait(t, done)
			if n := f.resolved.Load(); n != 1 {
				t.Errorf("resolved %d times, want 1", n)
			}
			if d := f.dialedAddrs(); len(d) != 0 {
				t.Errorf("dialed %v, want none", d)
			}
			if line := f.log.String(); !strings.Contains(line, "verdict=refused") {
				t.Errorf("log = %q, want a refused verdict", line)
			}
		})
	}
}

func TestServeRefusesAPublicAndAPrivateAnswer(t *testing.T) {
	f := newFixture(t)
	f.answers = []netip.Addr{publicAddr, netip.MustParseAddr("10.0.0.1")}
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
	if d := f.dialedAddrs(); len(d) != 0 {
		t.Errorf("dialed %v, want none", d)
	}
}

func TestServeRefusesWithoutInterfaceAddresses(t *testing.T) {
	f := newFixture(t)
	f.relay.LocalAddrs = func() ([]netip.Addr, error) { return nil, errors.New("no interfaces") }
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
	if d := f.dialedAddrs(); len(d) != 0 {
		t.Errorf("dialed %v, want none", d)
	}
}

func TestServeRefusesAnEmptyInterfaceList(t *testing.T) {
	f := newFixture(t)
	f.relay.LocalAddrs = func() ([]netip.Addr, error) { return nil, nil }
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
	if d := f.dialedAddrs(); len(d) != 0 {
		t.Errorf("dialed %v, want none", d)
	}
}

// TestServeReadsTheHostsInterfacesByDefault runs Serve with LocalAddrs
// unset, so with this machine's real interface addresses.
func TestServeReadsTheHostsInterfacesByDefault(t *testing.T) {
	for _, c := range []struct {
		name   string
		addr   netip.Addr
		status int
	}{
		{"a public address is dialed", publicAddr, http.StatusOK},
		{"loopback is refused", netip.MustParseAddr("127.0.0.1"), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.relay.LocalAddrs = nil
			f.answers = []netip.Addr{c.addr}
			guest, done := f.serve()
			if status, _ := connect(t, guest, "example.com:443"); status != c.status {
				t.Fatalf("status = %d, want %d", status, c.status)
			}
			if c.status == http.StatusOK {
				f.accept().Close()
			}
			guest.Close()
			wait(t, done)
		})
	}
}

func TestInterfaceAddrsHoldsLoopback(t *testing.T) {
	addrs, err := InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if a.Unmap().IsLoopback() {
			return
		}
	}
	t.Errorf("interfaceAddrs() = %v, want a loopback address among them", addrs)
}

func TestServeDialsPublicAddress(t *testing.T) {
	for _, c := range []struct {
		name string
		addr string
		dial string
	}{
		{"just below CGNAT", "100.63.255.255", "100.63.255.255:443"},
		{"just above CGNAT", "100.128.0.0", "100.128.0.0:443"},
		{"just below 172.16/12", "172.15.255.255", "172.15.255.255:443"},
		{"just above 172.16/12", "172.32.0.0", "172.32.0.0:443"},
		{"just above 198.18/15", "198.20.0.0", "198.20.0.0:443"},
		{"just above Teredo", "2001:1::1", "[2001:1::1]:443"},
		{"just above ORCHIDv2", "2001:30::1", "[2001:30::1]:443"},
		{"just above 3fff::/20", "3fff:1000::1", "[3fff:1000::1]:443"},
		{"a global IPv6 address", "2606:4700::1111", "[2606:4700::1111]:443"},
		{"an IPv4-mapped public address, dialed unmapped", "::ffff:8.8.8.8", "8.8.8.8:443"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.answers = []netip.Addr{netip.MustParseAddr(c.addr)}
			guest, done := f.serve()
			if status, _ := connect(t, guest, "example.com:443"); status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			f.accept().Close()
			guest.Close()
			wait(t, done)
			if d := f.dialedAddrs(); len(d) != 1 || d[0].String() != c.dial {
				t.Errorf("dialed %v, want [%s]", d, c.dial)
			}
		})
	}
}
