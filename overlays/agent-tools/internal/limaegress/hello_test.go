package limaegress

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func appendU16Bytes(b, v []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(v)))
	return append(b, v...)
}

// echConfigList returns an ECHConfigList (draft-ietf-tls-esni) of one
// X25519 config whose public name is publicName, which a client sends as
// its outer ClientHello's SNI.
func echConfigList(t *testing.T, publicName string) []byte {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var c []byte
	c = append(c, 1)                                      // config_id
	c = binary.BigEndian.AppendUint16(c, 0x0020)          // DHKEM(X25519, HKDF-SHA256)
	c = appendU16Bytes(c, key.PublicKey().Bytes())        // public_key
	c = appendU16Bytes(c, []byte{0x00, 0x01, 0x00, 0x01}) // HKDF-SHA256, AES-128-GCM
	c = append(c, 0)                                      // maximum_name_length
	c = append(c, byte(len(publicName)))
	c = append(c, publicName...)
	c = appendU16Bytes(c, nil) // extensions
	config := binary.BigEndian.AppendUint16(nil, 0xfe0d)
	config = appendU16Bytes(config, c)
	return appendU16Bytes(nil, config)
}

// oversizedHello is a TLS record holding the start of a ClientHello that
// claims 20000 bytes, longer than the relay peeks.
func oversizedHello() []byte {
	body := make([]byte, 16384)
	body[0] = 1 // client_hello
	body[1], body[2], body[3] = 0, 20000>>8, 20000&0xff
	b := []byte{0x16, 0x03, 0x01, 16384 >> 8, 16384 & 0xff}
	return append(b, body...)
}

func TestServeClosesOnTheClientHello(t *testing.T) {
	for _, c := range []struct {
		name   string
		hello  func(t *testing.T) []byte
		reason string
	}{
		{"a mismatched SNI", func(t *testing.T) []byte { return helloFor(t, "marker.example.com") }, "server name"},
		{"no SNI", func(t *testing.T) []byte {
			return clientHello(t, &tls.Config{InsecureSkipVerify: true})
		}, "no server name"},
		{"an ECH extension", func(t *testing.T) []byte {
			return clientHello(t, &tls.Config{
				ServerName:                     "example.com",
				MinVersion:                     tls.VersionTLS13,
				EncryptedClientHelloConfigList: echConfigList(t, "example.com"),
			})
		}, "encrypted_client_hello"},
		{"plain HTTP", func(*testing.T) []byte { return []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n") }, "ClientHello"},
		{"a ClientHello over 16 KiB", func(*testing.T) []byte { return oversizedHello() }, "ClientHello"},
		{"a real ClientHello over 16 KiB", func(t *testing.T) []byte {
			// ALPN pads crypto/tls's ClientHello past the peek, over two
			// records; it is otherwise valid for the CONNECT host.
			var protos []string
			for i := range 80 {
				protos = append(protos, fmt.Sprintf("%03d%s", i, strings.Repeat("p", 252)))
			}
			hello := fullClientHello(t, &tls.Config{ServerName: "example.com", NextProtos: protos})
			if len(hello) <= maxHello {
				t.Fatalf("the ClientHello is %d bytes, want over %d", len(hello), maxHello)
			}
			return hello
		}, "ClientHello"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			guest, done := f.serve()
			status, br := connect(t, guest, "example.com:443")
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			up := f.accept()
			hello := c.hello(t)
			if _, err := guest.Write(append(hello, "after-hello"...)); err != nil {
				t.Fatal(err)
			}
			// The half-closes on both sides end a relay that forwards
			// instead of refusing, so the test fails rather than hangs.
			guest.CloseWrite()
			if got, _ := io.ReadAll(up); len(got) != 0 {
				t.Errorf("upstream received %d bytes, want none", len(got))
			}
			up.CloseWrite()
			if got, _ := io.ReadAll(br); len(got) != 0 {
				t.Errorf("guest received %q after the 200, want the relay closed", got)
			}
			wait(t, done)
			line := f.log.String()
			if !strings.Contains(line, "verdict=refused") || !strings.Contains(line, c.reason) {
				t.Errorf("log = %q, want a refusal naming %q", line, c.reason)
			}
			if strings.Contains(line, "marker") {
				t.Errorf("log = %q holds the guest's server name", line)
			}
		})
	}
}

// fullClientHello returns every TLS record of a real crypto/tls client's
// ClientHello, which crypto/tls splits into records of at most 16 KiB.
func fullClientHello(t *testing.T, config *tls.Config) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		tls.Client(client, config).Handshake()
		client.Close()
	}()
	var records, handshake []byte
	for len(handshake) < 4 || len(handshake) < 4+(int(handshake[1])<<16|int(handshake[2])<<8|int(handshake[3])) {
		header := make([]byte, 5)
		if _, err := io.ReadFull(server, header); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, int(header[3])<<8|int(header[4]))
		if _, err := io.ReadFull(server, body); err != nil {
			t.Fatal(err)
		}
		records = append(append(records, header...), body...)
		handshake = append(handshake, body...)
	}
	return records
}

func TestServeTimesOutAfterTenSeconds(t *testing.T) {
	f := newFixture(t)
	var mu sync.Mutex
	var asked []time.Duration
	// A clock that records each timeout and never lets one pass.
	f.relay.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, d)
		return nil
	}
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	if _, err := guest.Write(helloFor(t, "example.com")); err != nil {
		t.Fatal(err)
	}
	guest.CloseWrite()
	io.ReadAll(up)
	up.Close()
	if err := wait(t, done); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []time.Duration{10 * time.Second, 10 * time.Second}; fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Errorf("timeouts %v, want %v: the request's, then the ClientHello's", asked, want)
	}
}

func TestServeForwardsAMatchingClientHello(t *testing.T) {
	// The same hello as the ECH case, without the ECH extension: shows
	// that refusal is the extension's alone.
	f := newFixture(t)
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	hello := clientHello(t, &tls.Config{ServerName: "Example.COM", MinVersion: tls.VersionTLS13})
	if _, err := guest.Write(hello); err != nil {
		t.Fatal(err)
	}
	guest.CloseWrite()
	got, err := io.ReadAll(up)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, hello) {
		t.Errorf("upstream received %d bytes, want the hello's %d", len(got), len(hello))
	}
	up.Close()
	if err := wait(t, done); err != nil {
		t.Errorf("Serve: %v", err)
	}
}

func TestServeClosesOnASlowClientHello(t *testing.T) {
	f := newFixture(t)
	var calls atomic.Int32
	// The header's timeout never passes; the hello's has already.
	f.relay.after = func(time.Duration) <-chan time.Time {
		if calls.Add(1) == 1 {
			return nil
		}
		return expire(0)
	}
	guest, done := f.serve()
	if status, _ := connect(t, guest, "example.com:443"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	if _, err := guest.Write(helloFor(t, "example.com")[:10]); err != nil {
		t.Fatal(err)
	}
	// A bound on a relay that waits instead of closing, so the test fails
	// rather than hangs; a working relay closes at once.
	up.SetReadDeadline(time.Now().Add(10 * time.Second))
	if got, _ := io.ReadAll(up); len(got) != 0 {
		t.Errorf("upstream received %d bytes, want none", len(got))
	}
	wait(t, done)
	if line := f.log.String(); !strings.Contains(line, "verdict=refused") || !strings.Contains(line, "timed out") {
		t.Errorf("log = %q, want a refusal for the timeout", line)
	}
}
