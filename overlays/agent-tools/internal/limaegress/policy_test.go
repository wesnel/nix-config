package limaegress

import (
	"slices"
	"testing"
)

func TestPolicyQUICServers(t *testing.T) {
	p, err := ParsePolicy([]byte(`{"allow":[],"deny":[],"quicServers":["us.quic.connect-endpoint.ngrok.com","eu.quic.connect-endpoint.ngrok.com"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Name{"us.quic.connect-endpoint.ngrok.com", "eu.quic.connect-endpoint.ngrok.com"}; !slices.Equal(p.QUICServers(), want) {
		t.Fatalf("QUICServers() = %v, want %v", p.QUICServers(), want)
	}
}

func TestParseEndpoint(t *testing.T) {
	for in, want := range map[string]Endpoint{
		"db.internal:5432": {Host: "db.internal", Port: 5432},
		"10.0.0.5:443":     {Host: "10.0.0.5", Port: 443},
		"[2001:db8::1]:22": {Host: "2001:db8::1", Port: 22},
		"a.b:65535":        {Host: "a.b", Port: 65535},
	} {
		t.Run(in, func(t *testing.T) {
			got, err := ParseEndpoint(in)
			if err != nil || got != want || got.String() != in {
				t.Fatalf("ParseEndpoint(%q) = %+v (%q), %v; want %+v", in, got, got.String(), err, want)
			}
		})
	}
	// One spelling per endpoint: the broker compares a dial's destination
	// with the endpoints as they are written.
	for _, in := range []string{
		"db.internal", "db.internal:0", "db.internal:0443", "db.internal:65536", "db.internal:+443",
		"db_internal:5432", "DB.internal:5432", "db.internal.:5432", "[fe80::1%en0]:5432",
		"[2001:DB8::1]:22", "127.1:22", ":22",
	} {
		t.Run("refuses "+in, func(t *testing.T) {
			if got, err := ParseEndpoint(in); err == nil {
				t.Fatalf("ParseEndpoint(%q) = %+v", in, got)
			}
		})
	}
}

func TestPolicy(t *testing.T) {
	policy, err := ParsePolicy([]byte(`{
		"allow": ["proxy.golang.org", "*.github.com", "*.ngrok.com", "Example.ORG."],
		"deny": ["evil.github.com"],
		"quicServers": ["us.quic.tunnel.example.net"]
	}`))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	for _, c := range []struct {
		name  string
		host  string
		allow bool
	}{
		{"an exact name is allowed", "proxy.golang.org", true},
		{"a name under an exact one is not", "x.proxy.golang.org", false},
		{"a wildcard allows a subdomain", "api.github.com", true},
		{"a wildcard allows a deeper subdomain", "a.b.github.com", true},
		{"a wildcard leaves the apex out", "github.com", false},
		{"a wildcard matches whole labels only", "evilgithub.com", false},
		{"a deny wildcard matches whole labels only", "xconnect-endpoint.ngrok.com", true},
		{"a pattern is matched lowercase without its dot", "example.org", true},
		{"an unlisted name is refused", "example.com", false},
		{"a deny beats a wildcard allow", "evil.github.com", false},
		{"the connect-endpoint apex is refused", "connect-endpoint.ngrok.com", false},
		{"a name under connect-endpoint is refused", "us.h2.connect-endpoint.ngrok.com", false},
		{"api.ngrok.com is refused", "api.ngrok.com", false},
		{"other names under an allowed wildcard stay allowed", "bin.ngrok.com", true},
		{"a QUIC server name is refused", "us.quic.tunnel.example.net", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			name, err := ParseName(c.host)
			if err != nil {
				t.Fatalf("ParseName(%q): %v", c.host, err)
			}
			if got := policy.Allows(name); got != c.allow {
				t.Errorf("Allows(%q) = %v, want %v", c.host, got, c.allow)
			}
		})
	}
}

func TestPolicyAllowsQUICServerOnlyWhenUnlisted(t *testing.T) {
	// The same allowlist without the QUIC server: shows the refusal above
	// comes from quicServers, not from the name being unlisted.
	policy, err := ParsePolicy([]byte(`{"allow": ["*.example.net"], "deny": [], "quicServers": []}`))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	name, err := ParseName("us.quic.tunnel.example.net")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Allows(name) {
		t.Fatal("Allows = false without quicServers, want true")
	}
	policy, err = ParsePolicy([]byte(`{"allow": ["*.example.net"], "deny": [], "quicServers": ["us.quic.tunnel.example.net"]}`))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if policy.Allows(name) {
		t.Fatal("Allows = true with the name in quicServers, want false")
	}
}

func TestParsePolicyRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		json string
	}{
		{"invalid JSON", `{"allow": [`},
		{"an unknown field", `{"allow": [], "deny": [], "quicServers": [], "denny": ["x.com"]}`},
		{"trailing data", `{"allow": [], "deny": [], "quicServers": []} {}`},
		{"a bare wildcard", `{"allow": ["*"], "deny": [], "quicServers": []}`},
		{"a wildcard inside a name", `{"allow": ["a.*.com"], "deny": [], "quicServers": []}`},
		{"an IPv4 literal", `{"allow": ["10.0.0.1"], "deny": [], "quicServers": []}`},
		{"an IPv6 literal", `{"allow": ["::1"], "deny": [], "quicServers": []}`},
		{"a name with a port", `{"allow": ["example.com:443"], "deny": [], "quicServers": []}`},
		{"an empty pattern", `{"allow": [""], "deny": [], "quicServers": []}`},
		{"a label starting with a hyphen", `{"allow": ["-a.com"], "deny": [], "quicServers": []}`},
		{"a bad deny pattern", `{"allow": [], "deny": ["a_b.com"], "quicServers": []}`},
		{"a wildcard QUIC server", `{"allow": [], "deny": [], "quicServers": ["*.example.net"]}`},
		{"a bad QUIC server", `{"allow": [], "deny": [], "quicServers": ["a..b"]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParsePolicy([]byte(c.json)); err == nil {
				t.Errorf("ParsePolicy(%s) succeeded, want an error", c.json)
			}
		})
	}
}

func TestParseName(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Name
	}{
		{"Example.COM", "example.com"},
		{"example.com.", "example.com"},
		{"a-b.c1.example", "a-b.c1.example"},
		{"localhost", "localhost"},
		{"0x7f.example", "0x7f.example"},
		{"a.0x1", "a.0x1"},
	} {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseName(c.in)
			if err != nil {
				t.Fatalf("ParseName(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("ParseName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	long := ""
	for range 64 {
		long += "a"
	}
	for _, in := range []string{
		"", ".", "example..com", "-a.com", "a-.com", "a_b.com", "a b.com",
		"1.2.3.4", "127.1", "example.123", "::1", "[::1]", "*.example.com",
		long + ".com", "é.com",
		// Non-ASCII letters that Unicode lowercases to ASCII: the Kelvin
		// sign to k, and the dotted capital I.
		"K.com", "İ.com",
		// Names inet_aton reads as an IPv4 address.
		"0x7f.0x1", "0x7f000001", "0177.1", "0X7F.1", "0x.0x",
	} {
		t.Run("refuses "+in, func(t *testing.T) {
			if got, err := ParseName(in); err == nil {
				t.Errorf("ParseName(%q) = %q, want an error", in, got)
			}
		})
	}
}
