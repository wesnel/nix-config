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
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.ngrok.com/ngrok/privatedial"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/limaegress"
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

// clear forgets the lines logged so far, so that has sees only later ones.
func (l *logs) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
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
		Servers:   []limaegress.Name{server},
		Endpoints: []limaegress.Endpoint{{Host: "db.internal", Port: 5432}},
		CA:        brokerCA.cert,
		Upstream: Upstream{
			Resolver: staticResolver{server: {upstreamLoopback}},
			RootCAs:  upstreamCA.pool,
		},
		Port:      f.shimPort,
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

// rawClient returns an HTTP/3 client that reaches the broker through the
// shim, as the guest's client does, but sends whatever request it is given.
func (f *fixture) rawClient(t *testing.T) *http3.Transport {
	t.Helper()
	shim := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(f.shimPort)}
	tr := &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: f.brokerCA.pool, ServerName: server},
		Dial: func(ctx context.Context, _ string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
			pc, err := net.ListenUDP("udp", nil)
			if err != nil {
				return nil, err
			}
			conn, err := quic.Dial(ctx, pc, shim, tlsCfg, cfg)
			if err != nil {
				pc.Close()
				return nil, err
			}
			context.AfterFunc(conn.Context(), func() { pc.Close() })
			return conn, nil
		},
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

// send sends one request through tr and returns its status.
func send(t *testing.T, ctx context.Context, tr *http3.Transport, req *http.Request) int {
	t.Helper()
	resp, err := tr.RoundTrip(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	resp.Body.Close()
	return resp.StatusCode
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

func TestPolicy(t *testing.T) {
	t.Run("a dial outside the endpoint allowlist is refused", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		d := f.client(t, server)
		connect(t, ctx, d)
		f.upstream.next(t) // the session
		conn, err := d.DialContext(ctx, "tcp", "db.internal:5433")
		if err == nil {
			conn.Close()
			t.Fatal("a dial to db.internal:5433, outside the allowlist, was relayed")
		}
		if !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("dial error %v, want a 403", err)
		}
		if !f.logs.has("refused", map[string]string{"reason": "endpoint", "endpoint": "db.internal:5433"}) {
			t.Fatalf("the dial failed (%v) without the broker refusing its endpoint", err)
		}
		f.upstream.noneSeen(t)
	})

	t.Run("get-host is relayed for an allowlisted host and refused for another", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		d := f.client(t, server)
		connect(t, ctx, d)
		f.upstream.next(t) // the session
		if exists, err := d.GetHost(ctx, "db.internal"); err != nil || !exists {
			t.Fatalf("GetHost(db.internal) = %v, %v; want true, nil", exists, err)
		}
		if got := f.upstream.next(t).path; got != "/get-host" {
			t.Fatalf("upstream saw %s, want /get-host", got)
		}
		if _, err := d.GetHost(ctx, "other.internal"); err == nil || !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("GetHost(other.internal) error %v, want a 403", err)
		}
		if !f.logs.has("refused", map[string]string{"reason": "host", "host": "other.internal"}) {
			t.Fatal("GetHost(other.internal) failed without the broker refusing its host")
		}
		f.upstream.noneSeen(t)
	})

	t.Run("an :authority other than the server name is refused", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		// The client names the shim by address, so its :authority is
		// 127.0.0.1:port while its SNI is the server's name.
		d := privatedial.New(privatedial.Config{
			QUICServerAddr: fmt.Sprintf("127.0.0.1:%d", f.shimPort),
			ForceProtocol:  privatedial.ProtocolQUIC,
			AuthToken:      "synthetic",
			TLSConfig:      &tls.Config{RootCAs: f.brokerCA.pool, ServerName: server},
		})
		t.Cleanup(func() { d.Close() })
		err := d.Connect(ctx)
		if err == nil || !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("Connect error %v, want a 403", err)
		}
		if !f.logs.has("refused", map[string]string{"reason": "authority"}) {
			t.Fatalf("Connect failed (%v) without the broker refusing its authority", err)
		}
		f.upstream.noneSeen(t)
	})
}

func TestRequestShape(t *testing.T) {
	f := newFixture(t, nil)
	ctx := testContext(t)
	tr := f.rawClient(t)
	base := fmt.Sprintf("https://%s:%d", server, f.shimPort)
	getHost := []byte{13, 0x0a, 11, 'd', 'b', '.', 'i', 'n', 't', 'e', 'r', 'n', 'a', 'l'}

	request := func(method, target string, body []byte) *http.Request {
		req, err := http.NewRequest(method, target, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	// The same client and body pass when the request has the right shape.
	if status := send(t, ctx, tr, request(http.MethodPost, base+"/get-host", getHost)); status != http.StatusOK {
		t.Fatalf("a well-formed get-host got %d, want 200", status)
	}
	if got := f.upstream.next(t).path; got != "/get-host" {
		t.Fatalf("upstream saw %s, want /get-host", got)
	}

	plainConnect := &http.Request{Method: http.MethodConnect, URL: &url.URL{Scheme: "https", Host: fmt.Sprintf("%s:%d", server, f.shimPort)}, Header: http.Header{}}
	extended := request(http.MethodConnect, base+"/dial", nil)
	extended.Proto = "websocket"
	for _, tc := range []struct {
		name   string
		req    *http.Request
		reason string
	}{
		{"a GET", request(http.MethodGet, base+"/get-host", nil), "method"},
		{"a PUT", request(http.MethodPut, base+"/get-host", getHost), "method"},
		{"a CONNECT", plainConnect, "method"},
		{"an extended CONNECT", extended, "method"},
		{"a query", request(http.MethodPost, base+"/get-host?x=1", getHost), "path"},
		{"an empty query", request(http.MethodPost, base+"/get-host?", getHost), "path"},
		{"another path", request(http.MethodPost, base+"/get-hosts", getHost), "path"},
		{"a trailing slash", request(http.MethodPost, base+"/get-host/", getHost), "path"},
		{"another case", request(http.MethodPost, base+"/Get-Host", getHost), "path"},
		{"a doubled slash", request(http.MethodPost, base+"//get-host", getHost), "path"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			f.logs.clear()
			if status := send(t, ctx, tr, tc.req); status != http.StatusForbidden {
				t.Fatalf("status %d, want 403", status)
			}
			if !f.logs.has("refused", map[string]string{"reason": tc.reason}) {
				t.Fatalf("no refusal for its %s", tc.reason)
			}
			f.upstream.noneSeen(t)
		})
	}
}

// The fields of a message, for the encoding tests.
func text(num protowire.Number, s string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, num, protowire.BytesType), s)
}

func varint(num protowire.Number, v uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, num, protowire.VarintType), v)
}

// delimited joins fields into one message behind its varint length, as the
// client's first message.
func delimited(fields ...[]byte) []byte {
	message := bytes.Join(fields, nil)
	return append(protowire.AppendVarint(nil, uint64(len(message))), message...)
}

func TestEncoding(t *testing.T) {
	f := newFixture(t, nil)
	ctx := testContext(t)
	tr := f.rawClient(t)
	base := fmt.Sprintf("https://%s:%d", server, f.shimPort)
	host, port := text(1, "db.internal"), varint(2, 5432)
	entry := text(3, string(bytes.Join([][]byte{text(1, "key"), text(2, "value")}, nil)))
	session := text(4, "")
	// A metadata entry whose field holds pad bytes, making a DialReq of
	// exactly size bytes.
	padded := func(size int) []byte {
		fixed := len(host) + len(port)
		for n := size - fixed - 3; n >= 0; n-- {
			if m := delimited(host, port, text(3, strings.Repeat("x", n))); len(m)-len(protowire.AppendVarint(nil, uint64(size))) == size {
				return m
			}
		}
		t.Fatalf("no DialReq of %d bytes", size)
		return nil
	}

	for _, tc := range []struct {
		name, path string
		body       []byte
		relayed    bool
	}{
		{"a dial with metadata entries and a session_req", pathDial, delimited(host, port, entry, entry, session), true},
		{"a dial whose first message is exactly 64 KiB", pathDial, padded(64 << 10), true},
		{"a get-host", pathGetHost, delimited(host), true},
		{"a session request with a client version and metadata", pathSession, delimited(text(1, "v1"), text(2, "m"), text(2, "n")), true},

		{"a dial with field 1 twice", pathDial, delimited(host, host, port), false},
		{"a dial whose last host is the allowlisted one", pathDial, delimited(text(1, "other.internal"), host, port), false},
		{"a dial whose last port is the allowlisted one", pathDial, delimited(host, varint(2, 22), port), false},
		{"a dial with a field 1 of the varint type", pathDial, delimited(host, varint(1, 7), port), false},
		{"a dial with a field 2 of the bytes type", pathDial, delimited(host, port, text(2, "5432")), false},
		{"a dial with field 4 twice", pathDial, delimited(host, port, session, session), false},
		{"a dial with an unknown field", pathDial, delimited(host, port, varint(5, 1)), false},
		{"a dial whose host is not lowercase", pathDial, delimited(text(1, "DB.internal"), port), false},
		{"a dial to port 0", pathDial, delimited(host, varint(2, 0)), false},
		{"a dial whose first message is one byte over 64 KiB", pathDial, padded(64<<10 + 1), false},
		{"a get-host whose last host is the allowlisted one", pathGetHost, delimited(text(1, "other.internal"), host), false},
		{"a get-host with a port", pathGetHost, delimited(host, port), false},
		{"a get-host followed by a second one", pathGetHost, append(delimited(host), delimited(text(1, "other.internal"))...), false},
		{"a get-host followed by a byte", pathGetHost, append(delimited(host), 0), false},
		{"a session request with an unknown field", pathSession, delimited(text(1, "v1"), varint(3, 1)), false},
		{"a session request with a client version of the varint type", pathSession, delimited(varint(1, 1)), false},
		{"a session request with two client versions", pathSession, delimited(text(1, "v1"), text(1, "v2")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.logs.clear()
			req, err := http.NewRequest(http.MethodPost, base+tc.path, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			status := send(t, ctx, tr, req)
			if !tc.relayed {
				if status != http.StatusForbidden {
					t.Fatalf("status %d, want 403", status)
				}
				if !f.logs.has("refused", map[string]string{"reason": "first message", "path": tc.path}) {
					t.Fatal("no refusal of its first message")
				}
				f.upstream.noneSeen(t)
				return
			}
			if status != http.StatusOK {
				t.Fatalf("status %d, want 200", status)
			}
			if got := f.upstream.next(t); got.path != tc.path || !bytes.Equal(got.first, tc.body) {
				t.Fatalf("upstream saw %s with a %d-byte first message, want %s with the %d bytes sent", got.path, len(got.first), tc.path, len(tc.body))
			}
		})
	}
}

func TestFirstMessageDeadline(t *testing.T) {
	f := newFixture(t, func(cfg *Config) { cfg.firstMessageTimeout = 100 * time.Millisecond })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// A first message that declares five bytes and sends none of them.
	body, stall := io.Pipe()
	defer stall.Close()
	go stall.Write([]byte{5})
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://%s:%d/get-host", server, f.shimPort), body)
	if err != nil {
		t.Fatal(err)
	}
	if status := send(t, ctx, f.rawClient(t), req); status != http.StatusForbidden {
		t.Fatalf("status %d, want 403", status)
	}
	if !f.logs.has("refused", map[string]string{"reason": "first message", "path": "/get-host"}) {
		t.Fatal("no refusal of the stalled first message")
	}
	f.upstream.noneSeen(t)
}

func TestAuthority(t *testing.T) {
	for _, tc := range []struct {
		authority string
		port      uint16
		ok        bool
	}{
		{server, 443, true},
		{server + ":443", 443, true},
		{server + ":4443", 4443, true},
		{server + ":4443", 443, false},
		{server + ":", 443, false},
		{server + ".", 443, false},
		{"US.QUIC.CONNECT-ENDPOINT.NGROK.COM:443", 443, false},
		{"eu.quic.connect-endpoint.ngrok.com:443", 443, false},
		{"user@" + server + ":443", 443, false},
	} {
		t.Run(fmt.Sprintf("%s with port %d", tc.authority, tc.port), func(t *testing.T) {
			if got := authorityMatches(tc.authority, server, tc.port); got != tc.ok {
				t.Fatalf("authorityMatches = %v, want %v", got, tc.ok)
			}
		})
	}
}

func TestRelayedHeaders(t *testing.T) {
	f := newFixture(t, nil)
	ctx := testContext(t)
	getHost := []byte{13, 0x0a, 11, 'd', 'b', '.', 'i', 'n', 't', 'e', 'r', 'n', 'a', 'l'}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("https://%s:%d/get-host", server, f.shimPort), bytes.NewReader(getHost))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer synthetic")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "probe/1")
	req.Header.Set("X-Probe", "guest")
	req.Header.Set("Cookie", "session=guest")
	if status := send(t, ctx, f.rawClient(t), req); status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	got := f.upstream.next(t).header
	for name, want := range map[string]string{"Authorization": "Bearer synthetic", "Content-Type": "application/octet-stream", "User-Agent": "probe/1"} {
		if got.Get(name) != want {
			t.Errorf("upstream %s %q, want %q", name, got.Get(name), want)
		}
	}
	for _, name := range []string{"X-Probe", "Cookie"} {
		if values, ok := got[name]; ok {
			t.Errorf("upstream got %s %q, which the broker must not relay", name, values)
		}
	}
}

func TestServe(t *testing.T) {
	brokerCA := newAuthority(t)
	broker, err := New(Config{Servers: []limaegress.Name{server}, CA: brokerCA.cert})
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
