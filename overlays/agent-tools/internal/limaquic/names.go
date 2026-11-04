package limaquic

import (
	"net/netip"
	"strings"
)

// ldhName reports whether s is a lowercase LDH host name: labels of 1 to
// 63 lowercase letters, digits and hyphens, none starting or ending with a
// hyphen, joined by dots, at most 253 bytes, with no trailing dot.
func ldhName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := range len(label) {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// endpointHost reports whether s names a private endpoint's host: a
// lowercase LDH name, or an IP literal written as netip writes it, with no
// zone, so that one address has one spelling.
func endpointHost(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Zone() == "" && a.String() == s
	}
	return ldhName(s)
}
