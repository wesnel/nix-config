package limaegress

import (
	"fmt"
	"net"
	"net/netip"
)

// globalUnicast6 holds every IPv6 address a guest may reach: global
// unicast, less refusedPrefixes.
var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// refusedPrefixes are the addresses a guest may never reach: those that
// are not global-scope unicast, and the IPv6 forms that embed or translate
// to an IPv4 address (IPv4-compatible, IPv4-translated, NAT64, 6to4,
// Teredo), whose IPv4 address the check would not see. IPv4-mapped
// addresses are unmapped before the check, so they are refused exactly
// when their IPv4 address is. The IPv6 prefixes outside globalUnicast6
// are refused by it as well, and listed to say why.
var refusedPrefixes = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // this network, unspecified
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // CGNAT
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.88.99.0/24",  // 6to4 relay anycast
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, broadcast
		"::/96",           // unspecified, loopback, IPv4-compatible
		"::1/128",         // loopback
		"::ffff:0:0:0/96", // IPv4-translated
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard-only
		"2001::/32",       // Teredo
		"2001:2::/48",     // benchmarking
		"2001:20::/28",    // ORCHIDv2
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4
		"3fff::/20",       // documentation
		"5f00::/16",       // SRv6 SIDs
		"fc00::/7",        // ULA
		"fe80::/10",       // link-local
		"fec0::/10",       // site-local
		"ff00::/8",        // multicast
	} {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

// CheckAddrs returns addrs unmapped, or a refusal if any of them is an
// IPv6 address outside globalUnicast6, is in refusedPrefixes, or is one of
// local, the host's own addresses (InterfaceAddrs): one bad answer refuses
// the host, so that the host cannot be made to reach a private address by
// an answer list that also holds a public one. An empty local, which no
// host has (each has loopback), means the addresses were not read, and
// refuses. It is the one check of an address the Lima guest's traffic may
// reach, for the CONNECT relay and the private-dial broker alike.
func CheckAddrs(addrs, local []netip.Addr) ([]netip.Addr, error) {
	if len(local) == 0 {
		return nil, refuse("the host has no interface addresses")
	}
	own := map[netip.Addr]bool{}
	for _, a := range local {
		own[a.Unmap().WithZone("")] = true
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if !a.IsValid() {
			return nil, refuse("an invalid address")
		}
		if a.Is6() && !globalUnicast6.Contains(a) {
			return nil, refuse("address %s is not in %s", a, globalUnicast6)
		}
		for _, p := range refusedPrefixes {
			if p.Contains(a) {
				return nil, refuse("address %s is in %s", a, p)
			}
		}
		if own[a] {
			return nil, refuse("address %s is the host's own", a)
		}
		out = append(out, a)
	}
	return out, nil
}

// InterfaceAddrs returns the addresses of the host's interfaces, or an
// error if one is not an IP address.
func InterfaceAddrs() ([]netip.Addr, error) {
	nets, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, n := range nets {
		ipnet, ok := n.(*net.IPNet)
		if !ok {
			return nil, fmt.Errorf("interface address %v is not an IP network", n)
		}
		a, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			return nil, fmt.Errorf("interface address %v is not an IP address", n)
		}
		out = append(out, a)
	}
	return out, nil
}
