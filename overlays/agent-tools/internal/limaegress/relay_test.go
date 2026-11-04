package limaegress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publicAddr and publicAddr6 stand for public addresses the resolver
// answers, which the test's dialer maps to its own loopback listener.
// They are global unicast, unlike the documentation ranges, which the
// relay refuses.
var (
	publicAddr  = netip.MustParseAddr("8.8.8.8")
	publicAddr6 = netip.MustParseAddr("2606:4700::1111")
)

// fixture runs one Relay over a real loopback TCP connection standing for
// the guest, and a real loopback listener standing for every upstream.
type fixture struct {
	t        *testing.T
	relay    *Relay
	answers  []netip.Addr
	resolved atomic.Int32
	mu       sync.Mutex
	dialed   []netip.AddrPort
	refuse   map[netip.AddrPort]bool
	upstream net.Listener
	log      lockedBuffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	policy, err := ParsePolicy([]byte(`{"allow": ["example.com", "*.example.net", "api.ngrok.com"], "deny": [], "quicServers": []}`))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fixture{t: t, answers: []netip.Addr{publicAddr}, upstream: ln, refuse: map[netip.AddrPort]bool{}}
	f.relay = &Relay{
		Policy: policy,
		// The resolver is a fake because a real lookup leaves the machine.
		Resolve: func(_ context.Context, _ Name) ([]netip.Addr, error) {
			f.resolved.Add(1)
			return f.answers, nil
		},
		// The dialer is a fake because the answers are not reachable
		// addresses: it records the address and connects to the listener.
		Dial: func(ctx context.Context, addr netip.AddrPort) (Upstream, error) {
			f.mu.Lock()
			f.dialed = append(f.dialed, addr)
			refused := f.refuse[addr]
			f.mu.Unlock()
			if refused {
				return nil, errors.New("connection refused")
			}
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", ln.Addr().String())
			if err != nil {
				return nil, err
			}
			return c.(*net.TCPConn), nil
		},
		Log: &f.log,
	}
	return f
}

// serve starts Serve on the relay's end of a new guest connection and
// returns the guest's end, and a channel that receives Serve's result.
func (f *fixture) serve() (*net.TCPConn, <-chan error) {
	f.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	defer ln.Close()
	guest, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		f.t.Fatal(err)
	}
	relayEnd, err := ln.Accept()
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { guest.Close(); relayEnd.Close() })
	done := make(chan error, 1)
	go func() { done <- f.relay.Serve(f.t.Context(), relayEnd.(*net.TCPConn)) }()
	return guest.(*net.TCPConn), done
}

// accept returns the next upstream connection the relay made.
func (f *fixture) accept() *net.TCPConn {
	f.t.Helper()
	c, err := f.upstream.Accept()
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c.(*net.TCPConn)
}

func (f *fixture) dialedAddrs() []netip.AddrPort {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]netip.AddrPort(nil), f.dialed...)
}

// wait returns Serve's result, failing the test if it has not returned.
func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
		return nil
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// clientHello returns the first TLS record of a real crypto/tls client's
// handshake for serverName: its ClientHello.
func clientHello(t *testing.T, config *tls.Config) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		tls.Client(client, config).Handshake()
		client.Close()
	}()
	header := make([]byte, 5)
	if _, err := io.ReadFull(server, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(header[3])<<8|int(header[4]))
	if _, err := io.ReadFull(server, body); err != nil {
		t.Fatal(err)
	}
	return append(header, body...)
}

func helloFor(t *testing.T, serverName string) []byte {
	t.Helper()
	return clientHello(t, &tls.Config{ServerName: serverName})
}

// connect sends a CONNECT for target and returns the response's status
// and a reader for the tunnel's bytes after it.
func connect(t *testing.T, guest net.Conn, target string) (int, *bufio.Reader) {
	t.Helper()
	if _, err := fmt.Fprintf(guest, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	return readStatus(t, guest)
}

func readStatus(t *testing.T, guest net.Conn) (int, *bufio.Reader) {
	t.Helper()
	br := bufio.NewReader(guest)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("reading the relay's response: %v", err)
	}
	return resp.StatusCode, br
}

func TestServeRelays(t *testing.T) {
	f := newFixture(t)
	guest, done := f.serve()
	status, br := connect(t, guest, "example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	hello := helloFor(t, "example.com")
	payload := append(hello, "guest-secret-payload"...)
	if _, err := guest.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := guest.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(up)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("upstream received %d bytes, want the guest's %d intact", len(got), len(payload))
	}
	// After the guest's half-close, the upstream can still answer.
	if _, err := up.Write([]byte("upstream-reply")); err != nil {
		t.Fatal(err)
	}
	up.CloseWrite()
	reply, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "upstream-reply" {
		t.Fatalf("guest received %q, want %q", reply, "upstream-reply")
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	line := f.log.String()
	want := fmt.Sprintf(`host="example.com" port=443 verdict=relayed reason="" up=%d down=14`+"\n", len(payload))
	if !strings.HasSuffix(line, want) || strings.Count(line, "\n") != 1 {
		t.Errorf("log = %q, want one line ending %q", line, want)
	}
	if strings.Contains(line, "secret") || strings.Contains(line, "reply") {
		t.Errorf("log %q holds payload", line)
	}
}

func TestServeHalfClosesFromUpstream(t *testing.T) {
	f := newFixture(t)
	guest, done := f.serve()
	status, br := connect(t, guest, "a.example.net:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	hello := helloFor(t, "a.example.net")
	if _, err := guest.Write(hello); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(up, make([]byte, len(hello))); err != nil {
		t.Fatal(err)
	}
	if _, err := up.Write([]byte("upstream-first")); err != nil {
		t.Fatal(err)
	}
	up.CloseWrite()
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "upstream-first" {
		t.Fatalf("guest received %q, want %q", got, "upstream-first")
	}
	// After the upstream's half-close, the guest can still send.
	if _, err := guest.Write([]byte("guest-after")); err != nil {
		t.Fatal(err)
	}
	guest.CloseWrite()
	rest, err := io.ReadAll(up)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "guest-after" {
		t.Fatalf("upstream received %q, want %q", rest, "guest-after")
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServeDialsTheResolvedAddress(t *testing.T) {
	f := newFixture(t)
	second := publicAddr6
	f.answers = []netip.Addr{publicAddr, second}
	f.refuse[netip.AddrPortFrom(publicAddr, 443)] = true
	guest, done := f.serve()
	status, _ := connect(t, guest, "example.com:443")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	up := f.accept()
	want := []netip.AddrPort{netip.AddrPortFrom(publicAddr, 443), netip.AddrPortFrom(second, 443)}
	if got := f.dialedAddrs(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("dialed %v, want %v", got, want)
	}
	if n := f.resolved.Load(); n != 1 {
		t.Errorf("resolved %d times, want 1", n)
	}
	up.Close()
	guest.Close()
	wait(t, done)
}

func TestServeFailsWhenNoAddressConnects(t *testing.T) {
	f := newFixture(t)
	f.refuse[netip.AddrPortFrom(publicAddr, 443)] = true
	guest, done := f.serve()
	status, _ := connect(t, guest, "example.com:443")
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if err := wait(t, done); err == nil {
		t.Error("Serve returned nil, want the dial error")
	}
	if line := f.log.String(); !strings.Contains(line, `host="example.com" port=443 verdict=failed`) {
		t.Errorf("log = %q, want a failed verdict", line)
	}
}

func TestServeRefusesRequest(t *testing.T) {
	big := strings.Repeat("a", 8192)
	for _, c := range []struct {
		name    string
		request string
	}{
		{"a GET", "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"a POST", "POST / HTTP/1.1\r\nHost: example.com\r\n\r\n"},
		{"HTTP/1.0", "CONNECT example.com:443 HTTP/1.0\r\n\r\n"},
		{"port 80", "CONNECT example.com:80 HTTP/1.1\r\n\r\n"},
		{"port 0443", "CONNECT example.com:0443 HTTP/1.1\r\n\r\n"},
		{"no port", "CONNECT example.com HTTP/1.1\r\n\r\n"},
		{"an IPv4 literal", "CONNECT 203.0.113.7:443 HTTP/1.1\r\n\r\n"},
		{"an IPv6 literal", "CONNECT [2001:db8::7]:443 HTTP/1.1\r\n\r\n"},
		{"a path target", "CONNECT /example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"},
		{"a bad name", "CONNECT exa_mple.com:443 HTTP/1.1\r\n\r\n"},
		{"a denied host", "CONNECT us.h2.connect-endpoint.ngrok.com:443 HTTP/1.1\r\n\r\n"},
		{"api.ngrok.com, though allowed", "CONNECT api.ngrok.com:443 HTTP/1.1\r\n\r\n"},
		{"an unlisted host", "CONNECT example.org:443 HTTP/1.1\r\n\r\n"},
		{"a request body", "CONNECT example.com:443 HTTP/1.1\r\nContent-Length: 3\r\n\r\nabc"},
		{"headers over 8 KiB", "CONNECT example.com:443 HTTP/1.1\r\nX: " + big + "\r\n\r\n"},
		{"headers of 8 KiB and one byte", headerOf(8193)},
		{"a malformed header", "CONNECT example.com:443 HTTP/1.1\r\nno colon\r\n\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			guest, done := f.serve()
			if _, err := io.WriteString(guest, c.request); err != nil {
				t.Fatal(err)
			}
			status, br := readStatus(t, guest)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", status)
			}
			if rest, _ := io.ReadAll(br); len(rest) != 0 {
				t.Errorf("after the 403 the relay sent %q, want it closed", rest)
			}
			wait(t, done)
			if n := f.resolved.Load(); n != 0 {
				t.Errorf("resolved %d times, want none", n)
			}
			if line := f.log.String(); !strings.Contains(line, "verdict=refused") {
				t.Errorf("log = %q, want a refused verdict", line)
			}
		})
	}
}

// headerOf returns a CONNECT request for example.com:443 of n bytes in
// all, padded with a header.
func headerOf(n int) string {
	head := "CONNECT example.com:443 HTTP/1.1\r\nX: "
	return head + strings.Repeat("a", n-len(head)-len("\r\n\r\n")) + "\r\n\r\n"
}

func TestServeAcceptsHeadersOf8KiB(t *testing.T) {
	f := newFixture(t)
	guest, done := f.serve()
	request := headerOf(8192)
	if len(request) != 8192 {
		t.Fatalf("request is %d bytes, want 8192", len(request))
	}
	if _, err := io.WriteString(guest, request); err != nil {
		t.Fatal(err)
	}
	if status, _ := readStatus(t, guest); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	f.accept().Close()
	guest.Close()
	wait(t, done)
}

// expire stands for a clock on which every timeout has already passed.
func expire(time.Duration) <-chan time.Time {
	c := make(chan time.Time)
	close(c)
	return c
}

func TestServeRefusesSlowHeaders(t *testing.T) {
	f := newFixture(t)
	f.relay.after = expire
	guest, done := f.serve()
	if _, err := io.WriteString(guest, "CONNECT example.com:443 HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	status, _ := readStatus(t, guest)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	wait(t, done)
	if line := f.log.String(); !strings.Contains(line, "verdict=refused") || !strings.Contains(line, "timed out") {
		t.Errorf("log = %q, want a refusal for the timeout", line)
	}
}

func TestServeLogsNoRequestText(t *testing.T) {
	for _, c := range []struct {
		name    string
		request string
	}{
		{"a malformed header", "CONNECT example.com:443 HTTP/1.1\r\nX-MARKER no colon\r\n\r\n"},
		{"a method", "MARKER example.com:443 HTTP/1.1\r\n\r\n"},
		{"a protocol", "CONNECT example.com:443 HTTP/1.MARKER\r\n\r\n"},
		{"a port", "CONNECT example.com:443MARKER HTTP/1.1\r\n\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			guest, done := f.serve()
			if _, err := io.WriteString(guest, c.request); err != nil {
				t.Fatal(err)
			}
			if status, _ := readStatus(t, guest); status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", status)
			}
			wait(t, done)
			line := f.log.String()
			if !strings.Contains(line, "verdict=refused") {
				t.Fatalf("log = %q, want a refused verdict", line)
			}
			if strings.Contains(line, "MARKER") {
				t.Errorf("log = %q holds the guest's text", line)
			}
		})
	}
}

func TestServeCapsTheLoggedTarget(t *testing.T) {
	long := strings.Repeat("a", 1000)
	for _, target := range []string{long, long + ":443"} {
		t.Run(fmt.Sprintf("%d bytes", len(target)), func(t *testing.T) {
			f := newFixture(t)
			guest, done := f.serve()
			if _, err := fmt.Fprintf(guest, "CONNECT %s HTTP/1.1\r\n\r\n", target); err != nil {
				t.Fatal(err)
			}
			if status, _ := readStatus(t, guest); status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", status)
			}
			wait(t, done)
			line := f.log.String()
			want := fmt.Sprintf("host=%q ", strings.Repeat("a", 255))
			if !strings.Contains(line, want) {
				t.Errorf("log = %q, want the host cut to 255 bytes", line)
			}
		})
	}
}

func TestLookupFQDN(t *testing.T) {
	var asked []string
	// The lookup is a fake: a real one would leave the machine.
	resolve := lookupFQDN(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		asked = append(asked, network+" "+host)
		return []netip.Addr{publicAddr}, nil
	})
	addrs, err := resolve(t.Context(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 || addrs[0] != publicAddr {
		t.Errorf("resolve = %v, want [%v]", addrs, publicAddr)
	}
	if want := []string{"ip example.com."}; fmt.Sprint(asked) != fmt.Sprint(want) {
		t.Errorf("looked up %q, want %q: fully qualified, so no search domain applies", asked, want)
	}
}
