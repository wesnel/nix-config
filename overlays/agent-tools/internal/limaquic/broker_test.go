package limaquic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.ngrok.com/ngrok/privatedial"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/udpframe"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	installFakeDNS()
	os.Exit(m.Run())
}

// server is the private-dial QUIC server name the tests' policy lists;
// the fake DNS resolves it to loopback for the client.
const server = "us.quic.connect-endpoint.ngrok.com"

type authority struct {
	cert tls.Certificate
	pool *x509.CertPool
}

func newAuthority(t *testing.T) authority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Valid long enough for the leaf tests' clock to run past a leaf's life.
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(72 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return authority{cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool: pool}
}

func (a authority) issue(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert.Leaf, &key.PublicKey, a.cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// seen is what the upstream saw of one request.
type seen struct {
	method, path, authority, remote string
	header                          http.Header
	// first is the request body's first delimited message, length prefix
	// included.
	first []byte
}

// upstream is a synthetic private-dial server: it answers /session with a
// SessionAck and holds the stream, /get-host with an empty GetHostResp,
// and /dial with an empty DialResp, then echoes the rest of the body and
// ends with the error trailer a real server sends when a dial fails.
type upstream struct {
	port     uint16
	requests chan seen
}

func startUpstream(t *testing.T, ca authority) *upstream {
	t.Helper()
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := &upstream{port: uint16(packet.LocalAddr().(*net.UDPAddr).Port), requests: make(chan seen, 64)}
	srv := &http3.Server{
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{ca.issue(t, server)}, MinVersion: tls.VersionTLS13},
		Handler:   http.HandlerFunc(up.serve),
		// quic-go v0.59.1 logs through a nil Logger when it writes
		// trailers, and panics.
		Logger: slog.New(slog.DiscardHandler),
	}
	go srv.Serve(packet)
	t.Cleanup(func() {
		srv.Close()
		packet.Close()
	})
	return up
}

func (up *upstream) serve(w http.ResponseWriter, r *http.Request) {
	body := bufio.NewReader(r.Body)
	first, err := readDelimited(body)
	up.requests <- seen{method: r.Method, path: r.URL.RequestURI(), authority: r.Host, remote: r.RemoteAddr, header: r.Header.Clone(), first: first}
	if err != nil {
		http.Error(w, "first message", http.StatusBadRequest)
		return
	}
	flush := func() { http.NewResponseController(w).Flush() }
	switch r.URL.Path {
	case "/session":
		// SessionAck{server_id: "fixture"}.
		w.Write([]byte{9, 0x0a, 7, 'f', 'i', 'x', 't', 'u', 'r', 'e'})
		flush()
		<-r.Context().Done()
	case "/get-host":
		w.Write([]byte{0})
	case "/dial":
		w.Header().Set("Trailer", "Ngrok-Dial-Error-Code, Ngrok-Dial-Error-Message")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0})
		flush()
		buf := make([]byte, 1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				flush()
			}
			if err != nil {
				break
			}
		}
		w.Header().Set("Ngrok-Dial-Error-Code", "ERR_FIXTURE")
		w.Header().Set("Ngrok-Dial-Error-Message", "synthetic trailer")
	}
}

func readDelimited(r *bufio.Reader) ([]byte, error) {
	length, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if length > 1<<20 {
		return nil, errors.New("too long")
	}
	message := make([]byte, length)
	if _, err := io.ReadFull(r, message); err != nil {
		return nil, err
	}
	return append(binary.AppendUvarint(nil, length), message...), nil
}

// noneSeen fails the test if the upstream saw any request.
func (up *upstream) noneSeen(t *testing.T) {
	t.Helper()
	select {
	case s := <-up.requests:
		t.Fatalf("upstream saw %s %s", s.method, s.path)
	default:
	}
}

func (up *upstream) next(t *testing.T) seen {
	t.Helper()
	select {
	case s := <-up.requests:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("upstream saw no request")
		return seen{}
	}
}

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := r[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addrs, nil
}

var upstreamLoopback = netip.MustParseAddr("127.0.0.1")

// logs collects the broker's log lines, as JSON objects.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// has reports whether a line with msg was logged whose every attribute in
// attrs has that value.
func (l *logs) has(msg string, attrs map[string]string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for line := range strings.SplitSeq(l.buf.String(), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil || record["msg"] != msg {
			continue
		}
		matched := true
		for k, v := range attrs {
			if fmt.Sprint(record[k]) != v {
				matched = false
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// fixture is the path the tests drive: the real privatedial client, over
// UDP to the guest shim, framed over TCP to the broker, which relays over
// HTTP/3 to the synthetic upstream.
type fixture struct {
	shimPort uint16
	brokerCA authority
	upstream *upstream
	logs     *logs
	// flows counts the flows the broker has been handed.
	flows chan struct{}
}

func newFixture(t *testing.T, configure func(*Config)) *fixture {
	t.Helper()
	brokerCA, upstreamCA := newAuthority(t), newAuthority(t)
	f := &fixture{brokerCA: brokerCA, upstream: startUpstream(t, upstreamCA), logs: &logs{}, flows: make(chan struct{}, 64)}

	shimPacket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.shimPort = uint16(shimPacket.LocalAddr().(*net.UDPAddr).Port)

	cfg := Config{
		Servers:   []string{server},
		Endpoints: []string{"db.internal:5432"},
		CA:        brokerCA.cert,
		Upstream: Upstream{
			Resolver: staticResolver{server: {upstreamLoopback}},
			RootCAs:  upstreamCA.pool,
		},
		Logger:    slog.New(slog.NewJSONHandler(f.logs, nil)),
		dialPort:  f.upstream.port,
		allowAddr: func(a netip.Addr) bool { return a == upstreamLoopback },
	}
	if configure != nil {
		configure(&cfg)
	}
	broker, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var serving sync.WaitGroup
	serving.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.flows <- struct{}{}
			serving.Go(func() {
				defer conn.Close()
				if err := broker.Serve(ctx, conn); err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("Serve: %v", err)
				}
			})
		}
	})
	var dialer net.Dialer
	shim := &udpframe.Shim{
		Dial: func(ctx context.Context) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", ln.Addr().String()) },
		Idle: time.Minute,
	}
	serving.Go(func() { shim.Serve(ctx, shimPacket) })
	t.Cleanup(func() {
		cancel()
		ln.Close()
		serving.Wait()
		shimPacket.Close()
		if t.Failed() {
			t.Logf("broker log:\n%s", f.logs)
		}
	})
	return f
}

// client returns the real private-dial client, pointed at the shim under
// the server name, and presenting serverName as its SNI.
func (f *fixture) client(t *testing.T, serverName string) *privatedial.Dialer {
	t.Helper()
	d := privatedial.New(privatedial.Config{
		QUICServerAddr: fmt.Sprintf("%s:%d", server, f.shimPort),
		ForceProtocol:  privatedial.ProtocolQUIC,
		AuthToken:      "synthetic",
		TLSConfig:      &tls.Config{RootCAs: f.brokerCA.pool, ServerName: serverName},
	})
	t.Cleanup(func() { d.Close() })
	return d
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func connect(t *testing.T, ctx context.Context, d *privatedial.Dialer) {
	t.Helper()
	if err := d.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := d.Protocol(); got != privatedial.ProtocolQUIC {
		t.Fatalf("protocol %v, want QUIC", got)
	}
}

func echo(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != message {
		t.Fatalf("echo %q, want %q", got, message)
	}
}

// dialReq is DialReq{host: "db.internal", port: 5432}, delimited, as the
// client encodes it.
var dialReq = []byte{16, 0x0a, 11, 'd', 'b', '.', 'i', 'n', 't', 'e', 'r', 'n', 'a', 'l', 0x10, 0xb8, 0x2a}

func TestRelay(t *testing.T) {
	t.Run("a dial echoes both ways, half-closes and ends with the error trailer", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		d := f.client(t, server)
		connect(t, ctx, d)
		session := f.upstream.next(t)
		if session.method != http.MethodPost || session.path != "/session" || session.authority != server {
			t.Fatalf("upstream session request %s %s at %s, want POST /session at %s", session.method, session.path, session.authority, server)
		}
		if got := session.header.Get("Authorization"); got != "Bearer synthetic" {
			t.Fatalf("upstream Authorization %q, want the client's", got)
		}

		conn, err := d.DialContext(ctx, "tcp", "db.internal:5432")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		echo(t, conn, "hello")
		dial := f.upstream.next(t)
		if dial.path != "/dial" || !bytes.Equal(dial.first, dialReq) {
			t.Fatalf("upstream dial %s with first message %x, want /dial with %x", dial.path, dial.first, dialReq)
		}

		if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(conn)
		var trailer privatedial.Error
		if !errors.As(err, &trailer) || trailer.Code() != "ERR_FIXTURE" {
			t.Fatalf("read after half-close: %v, want the ERR_FIXTURE trailer", err)
		}
	})

	t.Run("two concurrent dials on one flow share one upstream connection", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		d := f.client(t, server)
		connect(t, ctx, d)
		first, err := d.DialContext(ctx, "tcp", "db.internal:5432")
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		second, err := d.DialContext(ctx, "tcp", "db.internal:5432")
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		echo(t, second, "two")
		echo(t, first, "one")

		remotes := map[string]bool{}
		for range 3 {
			remotes[f.upstream.next(t).remote] = true
		}
		if len(remotes) != 1 {
			t.Fatalf("the session and two dials reached upstream from %d connections, %v; want 1", len(remotes), remotes)
		}
		if flows := len(f.flows); flows != 1 {
			t.Fatalf("the client used %d flows, want 1", flows)
		}
	})
}

func TestUpstreamVerification(t *testing.T) {
	// The upstream's leaf is signed by its own CA; the broker trusts
	// another, so the handshake must fail.
	f := newFixture(t, func(cfg *Config) { cfg.Upstream.RootCAs = newAuthority(t).pool })
	ctx := testContext(t)
	err := f.client(t, server).Connect(ctx)
	if err == nil {
		t.Fatal("the session was relayed to an upstream whose certificate no trusted CA signed")
	}
	if !f.logs.has("upstream failed", map[string]string{"path": "/session"}) {
		t.Fatalf("Connect failed (%v) without the broker failing to reach upstream", err)
	}
	f.upstream.noneSeen(t)
}

func TestServerName(t *testing.T) {
	f := newFixture(t, nil)
	ctx := testContext(t)
	err := f.client(t, "eu.quic.connect-endpoint.ngrok.com").Connect(ctx)
	if err == nil {
		t.Fatal("a client with an SNI outside the server list connected")
	}
	if !f.logs.has("refused", map[string]string{"reason": "server name", "sni": "eu.quic.connect-endpoint.ngrok.com"}) {
		t.Fatalf("Connect failed (%v) without the broker refusing the server name", err)
	}
	f.upstream.noneSeen(t)
}

func TestUpstreamAddress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addrs     []string
		allowAddr func(netip.Addr) bool
	}{
		{name: "a loopback answer fails the public-address check", addrs: []string{"127.0.0.1"}},
		{
			name:      "one refused answer among allowed ones refuses them all",
			addrs:     []string{"127.0.0.1", "127.0.0.2"},
			allowAddr: func(a netip.Addr) bool { return a == upstreamLoopback },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var addrs []netip.Addr
			for _, a := range tc.addrs {
				addrs = append(addrs, netip.MustParseAddr(a))
			}
			f := newFixture(t, func(cfg *Config) {
				cfg.Upstream.Resolver = staticResolver{server: addrs}
				cfg.allowAddr = tc.allowAddr
			})
			ctx := testContext(t)
			err := f.client(t, server).Connect(ctx)
			if err == nil {
				t.Fatal("the session reached a refused upstream address")
			}
			if !f.logs.has("upstream refused", map[string]string{"host": server}) {
				t.Fatalf("Connect failed (%v) without the broker refusing the upstream address", err)
			}
			f.upstream.noneSeen(t)
		})
	}
}

func TestServe(t *testing.T) {
	brokerCA := newAuthority(t)
	broker, err := New(Config{Servers: []string{server}, CA: brokerCA.cert})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the flow's end ends it", func(t *testing.T) {
		near, far := net.Pipe()
		defer near.Close()
		done := make(chan error, 1)
		go func() { done <- broker.Serve(t.Context(), near) }()
		far.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Serve at the flow's end: %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Serve outlived its flow")
		}
	})

	t.Run("its context's end ends it", func(t *testing.T) {
		near, far := net.Pipe()
		defer near.Close()
		defer far.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- broker.Serve(ctx, near) }()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Serve after its context ended: %v, want context.Canceled", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Serve outlived its context")
		}
	})
}
