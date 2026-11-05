package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/wesnel/agent-tools/internal/limaegress"
	"github.com/wesnel/agent-tools/internal/limavm"
	"github.com/wesnel/agent-tools/internal/udpframe"
)

// managedConfig writes a managed VM configuration whose Lima home is a
// temporary directory, and returns it parsed.
func managedConfig(t *testing.T) *limavm.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "lima")
	data := `{"home":"/Users/x","limaHome":"` + home + `","instance":"ngrok-agent","workdir":"src/ngrok-agent",
	"limactl":"/nix/store/l/bin/limactl","qemu":"/nix/store/q/bin/qemu-system-aarch64",
	"guard":"/nix/store/g/bin/qemu-system-aarch64","limaMcp":"/nix/store/m/bin/lima-mcp",
	"template":"/nix/store/t.yaml","shim":"/nix/store/s/bin/lima-udp-shim","deniedReads":["/Users/x/.ssh"],
	"connect":"192.168.5.100:3128","quic":"192.168.5.101:443",
	"egress":{"allow":["github.com"],"deny":[],"quicServers":["us.quic.connect-endpoint.ngrok.com"]},
	"privateDialEndpoints":["db.internal:5432"]}`
	c, err := limavm.ParseConfig([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// guestPair is the two ends of a Unix socket pair, as slirp gives a
// guestfwd command: the relay's end, which is its stdin, and the guest's.
func guestPair(t *testing.T) (relay, guest net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]net.Conn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "socketpair")
		if conns[i], err = net.FileConn(f); err != nil {
			t.Fatal(err)
		}
		f.Close()
		t.Cleanup(func() { conns[i].Close() })
	}
	return conns[0], conns[1]
}

// holdSlots takes n slots in dir, as relays already running would.
func holdSlots(t *testing.T, dir string, n int) {
	t.Helper()
	slots := limaegress.FlockSlots{Dir: dir, N: n}
	for range n {
		release, err := slots.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
}

func TestEgressConnect(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		held       int
		logged     string
	}{
		{"refuses a host outside the configured policy", "example.com", 0, `host="example.com"`},
		{"refuses at once when 64 relays run", "github.com", 64, "no free relay slot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := managedConfig(t)
			holdSlots(t, c.Slots(), tc.held)
			relay, guest := guestPair(t)
			var log bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- runEgress(t.Context(), "connect", c, relay, &log) }()
			if err := guest.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(guest, "CONNECT "+tc.host+":443 HTTP/1.1\r\nHost: "+tc.host+":443\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			line, err := bufio.NewReader(guest).ReadString('\n')
			if err != nil || !strings.HasPrefix(line, "HTTP/1.1 403") {
				t.Fatalf("guest read %q, %v; want a 403", line, err)
			}
			<-done
			if !strings.Contains(log.String(), tc.logged) {
				t.Errorf("log %q does not say %s", log.String(), tc.logged)
			}
		})
	}
}

// quicHandshake dials the broker over guest as the guest's client does,
// through the shim's framing, and returns the handshake's error.
func quicHandshake(t *testing.T, c *limavm.Config, guest net.Conn, name string) error {
	t.Helper()
	ca, err := limavm.LoadCA(c.CACert(), c.CAKey())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Leaf)
	if err := guest.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	broker := &net.UDPAddr{IP: net.IPv4(127, 0, 1, 1), Port: 443}
	pc := udpframe.NewPacketConn(guest, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, broker)
	defer pc.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := quic.Dial(ctx, pc, broker, &tls.Config{RootCAs: roots, ServerName: name, NextProtos: []string{"h3"}}, nil)
	if err == nil {
		conn.CloseWithError(0, "")
	}
	return err
}

func TestEgressQUIC(t *testing.T) {
	t.Run("serves a configured server with the instance CA", func(t *testing.T) {
		c := managedConfig(t)
		if err := limavm.EnsureCA(t.Context(), c.CACert(), c.CAKey(), c.QUICServers); err != nil {
			t.Fatal(err)
		}
		holdSlots(t, c.QUICSlots(), 15)
		relay, guest := guestPair(t)
		go runEgress(t.Context(), "quic", c, relay, io.Discard)
		if err := quicHandshake(t, c, guest, "us.quic.connect-endpoint.ngrok.com"); err != nil {
			t.Fatalf("handshake with the 16th slot free: %v", err)
		}
	})

	t.Run("exits at once when 16 flows run", func(t *testing.T) {
		c := managedConfig(t)
		if err := limavm.EnsureCA(t.Context(), c.CACert(), c.CAKey(), c.QUICServers); err != nil {
			t.Fatal(err)
		}
		holdSlots(t, c.QUICSlots(), 16)
		relay, _ := guestPair(t)
		done := make(chan error, 1)
		go func() { done <- runEgress(t.Context(), "quic", c, relay, io.Discard) }()
		select {
		case err := <-done:
			if !errors.Is(err, limaegress.ErrNoSlot) {
				t.Fatalf("err = %v; want %v", err, limaegress.ErrNoSlot)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a 17th flow is being served")
		}
	})

	t.Run("refuses to serve without the CA", func(t *testing.T) {
		c := managedConfig(t)
		relay, _ := guestPair(t)
		if err := runEgress(t.Context(), "quic", c, relay, io.Discard); err == nil {
			t.Fatal("served without a CA")
		}
	})
}
