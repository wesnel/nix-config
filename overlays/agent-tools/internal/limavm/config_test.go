package limavm

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// configJSON is a configuration as Nix renders one, with the probe's
// paths, which the guard tests' argv fixtures name.
func configJSON(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	raw := map[string]any{
		"home":        "/Users/wgn",
		"limaHome":    "/private/tmp/lima-probe",
		"instance":    "probe",
		"workdir":     "src/ngrok-agent",
		"limactl":     "/nix/store/8prndgcnh1gqg2kaigkcp097wzisqlid-lima-2.2.0/bin/limactl",
		"qemu":        "/nix/store/59hhc8zlvdlbcls7dkbhkhw9afwb6mps-qemu-11.1.2/bin/qemu-system-aarch64",
		"guard":       "/private/tmp/claude/limaprobe/bin/qemu-system-aarch64",
		"limaMcp":     "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-tools/bin/lima-mcp",
		"template":    "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-ngrok-agent.yaml",
		"shim":        "/nix/store/cccccccccccccccccccccccccccccccc-lima-udp-shim/bin/lima-udp-shim",
		"deniedReads": []any{"/Users/wgn/.ssh", "/private/var/folders/*/*/T/secrets.d", "/Users/wgn/Library/Application Support/ngrok"},
		"connect":     "192.168.5.100:3128",
		"quic":        "192.168.5.101:443",
		"egress": map[string]any{
			"allow":       []any{"github.com", "*.githubusercontent.com"},
			"deny":        []any{},
			"quicServers": []any{"us.quic.connect-endpoint.ngrok.com"},
		},
		"privateDialEndpoints": []any{"db.internal:5432"},
	}
	if edit != nil {
		edit(raw)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseConfig(t *testing.T) {
	t.Run("a rendered configuration", func(t *testing.T) {
		c, err := ParseConfig(configJSON(t, nil))
		if err != nil {
			t.Fatal(err)
		}
		if c.InstanceDir() != "/private/tmp/lima-probe/probe" || c.CAKey() != "/private/tmp/lima-probe/_mcp/ca.key" ||
			c.ConfigLink() != "/private/tmp/lima-probe/_mcp/config.json" || c.BinLink() != "/private/tmp/lima-probe/_mcp/bin/lima-mcp" {
			t.Errorf("paths: %s %s %s %s", c.InstanceDir(), c.CAKey(), c.ConfigLink(), c.BinLink())
		}
		if c.Connect != netip.MustParseAddrPort("192.168.5.100:3128") || c.QUIC != netip.MustParseAddrPort("192.168.5.101:443") {
			t.Errorf("guestfwd endpoints %v %v", c.Connect, c.QUIC)
		}
		if !slices.Equal(c.QUICServers, []string{"us.quic.connect-endpoint.ngrok.com"}) || !slices.Equal(c.Endpoints, []string{"db.internal:5432"}) {
			t.Errorf("private dial: %v %v", c.QUICServers, c.Endpoints)
		}
		if !c.Egress.Allows("github.com") || c.Egress.Allows("us.quic.connect-endpoint.ngrok.com") {
			t.Error("the egress policy is not the one configured, with its QUIC servers denied")
		}
		if !slices.Equal(c.Firmware(), []string{"/private/tmp/claude/limaprobe/share/qemu", "/nix/store/59hhc8zlvdlbcls7dkbhkhw9afwb6mps-qemu-11.1.2/share/qemu"}) {
			t.Errorf("firmware directories %v", c.Firmware())
		}
	})

	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"an unknown field", func(m map[string]any) { m["extra"] = true }, "unknown field"},
		{"a missing path", func(m map[string]any) { delete(m, "limactl") }, "limactl"},
		{"a missing home", func(m map[string]any) { delete(m, "home") }, "home"},
		{"a relative path", func(m map[string]any) { m["qemu"] = "bin/qemu" }, "qemu"},
		{"an unclean path", func(m map[string]any) { m["limaHome"] = "/private/tmp/../lima-probe" }, "limaHome"},
		{"a comma, which would split QEMU's netdev option", func(m map[string]any) { m["limaHome"] = "/tmp/a,b" }, "limaHome"},
		{"a space, which would split the guestfwd command", func(m map[string]any) { m["limaHome"] = "/tmp/a b" }, "limaHome"},
		{"a quote in a denied path, which would end the Seatbelt string", func(m map[string]any) { m["deniedReads"] = []any{`/a"b`} }, "deniedReads"},
		{"a relative denied path", func(m map[string]any) { m["deniedReads"] = []any{"~/.ssh"} }, "deniedReads"},
		{"an instance name with a slash", func(m map[string]any) { m["instance"] = "a/b" }, "instance"},
		{"a workdir leaving the guest home", func(m map[string]any) { m["workdir"] = "../root" }, "workdir"},
		{"an absolute workdir", func(m map[string]any) { m["workdir"] = "/root" }, "workdir"},
		{"a relay outside the guest network", func(m map[string]any) { m["connect"] = "10.0.0.1:3128" }, "connect"},
		{"a relay at the slirp gateway", func(m map[string]any) { m["quic"] = "192.168.5.2:443" }, "quic"},
		{"both relays at one address", func(m map[string]any) { m["quic"] = "192.168.5.100:3128" }, "quic"},
		{"a bad egress pattern", func(m map[string]any) { m["egress"].(map[string]any)["allow"] = []any{"*"} }, "egress"},
		{"an unknown egress field", func(m map[string]any) { m["egress"].(map[string]any)["hosts"] = []any{} }, "egress"},
		{"no QUIC server", func(m map[string]any) { m["egress"].(map[string]any)["quicServers"] = []any{} }, "quicServers"},
		{"an endpoint without a port", func(m map[string]any) { m["privateDialEndpoints"] = []any{"db.internal"} }, "privateDialEndpoints"},
		{"an endpoint port with a leading zero", func(m map[string]any) { m["privateDialEndpoints"] = []any{"db.internal:0443"} }, "privateDialEndpoints"},
		{"an endpoint port over 65535", func(m map[string]any) { m["privateDialEndpoints"] = []any{"db.internal:65536"} }, "privateDialEndpoints"},
		{"an endpoint port in another base", func(m map[string]any) { m["privateDialEndpoints"] = []any{"db.internal:+443"} }, "privateDialEndpoints"},
		{"an endpoint host that is not LDH", func(m map[string]any) { m["privateDialEndpoints"] = []any{"db_internal:5432"} }, "privateDialEndpoints"},
		{"an uppercase endpoint host", func(m map[string]any) { m["privateDialEndpoints"] = []any{"DB.internal:5432"} }, "privateDialEndpoints"},
		{"an IPv6 zone in an endpoint", func(m map[string]any) { m["privateDialEndpoints"] = []any{"[fe80::1%en0]:5432"} }, "privateDialEndpoints"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := ParseConfig(configJSON(t, tc.edit))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want one naming %q", err, tc.want)
			}
		})
	}

	t.Run("refuses data after the object", func(t *testing.T) {
		if _, err := ParseConfig(append(configJSON(t, nil), []byte("{}")...)); err == nil {
			t.Fatal("trailing data parsed")
		}
	})
}
