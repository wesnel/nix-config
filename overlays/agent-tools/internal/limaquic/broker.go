// Package limaquic is the host's private-dial broker for the Lima guest,
// which is untrusted. The guest's private-dial client speaks HTTP/3 to what
// it takes for ngrok's QUIC server; its datagrams reach the host as one
// framed flow per QUIC connection (internal/udpframe), and Broker.Serve
// terminates that connection, with a leaf it mints for the server's name
// from the guest's CA, and relays each request to the real server over a
// separately verified HTTP/3 connection of its own.
package limaquic

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/wesnel/agent-tools/internal/limaegress"
	"github.com/wesnel/agent-tools/internal/udpframe"
)

// Resolver looks up a host's addresses; *net.Resolver is one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Upstream says how the broker reaches the real private-dial server.
type Upstream struct {
	// Resolver resolves the server's name on the host; nil is
	// net.DefaultResolver.
	Resolver Resolver
	// RootCAs verify the server; nil is the system's roots.
	RootCAs *x509.CertPool
}

// Config is a broker's policy and upstream, which New checks once.
type Config struct {
	// Servers are the private-dial QUIC server names, such as
	// us.quic.connect-endpoint.ngrok.com: the only names the broker
	// serves, and the only ones it relays to. limaegress.Policy's
	// QUICServers are these.
	Servers []limaegress.Name
	// Endpoints are the private endpoints a dial may reach.
	Endpoints []limaegress.Endpoint
	// CA signs the leaf the broker presents for each server name; the
	// guest trusts it.
	CA tls.Certificate
	// Port is the port the guest's client names in :authority, the shim's
	// UDP port; 0 is 443. An :authority may also leave it out.
	Port uint16
	// Upstream is how the broker reaches the servers.
	Upstream Upstream
	// Logger gets a line for each refusal and each relayed request, never
	// a credential or a body; nil logs nothing.
	Logger *slog.Logger

	// For tests: the upstream's port in place of 443, and the check of an
	// upstream address in place of limaegress.CheckAddrs with the host's
	// own addresses.
	dialPort  uint16
	allowAddr func(netip.Addr) bool
	// For tests: how long a request's first message may take in place of
	// ten seconds.
	firstMessageTimeout time.Duration
}

// Broker serves flows under one Config.
type Broker struct {
	servers   map[string]bool
	endpoints map[string]bool // host:port
	hosts     map[string]bool // the endpoints' hosts
	port      uint16
	leaves    *leaves
	resolver  Resolver
	rootCAs   *x509.CertPool
	dialPort  uint16
	allowAddr func(netip.Addr) bool
	log       *slog.Logger
	// firstMessageTimeout bounds the wait for a request's first message.
	firstMessageTimeout time.Duration
}

// New checks cfg and returns its broker.
func New(cfg Config) (*Broker, error) {
	b := &Broker{
		servers:             map[string]bool{},
		endpoints:           map[string]bool{},
		hosts:               map[string]bool{},
		port:                cfg.Port,
		resolver:            cfg.Upstream.Resolver,
		rootCAs:             cfg.Upstream.RootCAs,
		dialPort:            cfg.dialPort,
		allowAddr:           cfg.allowAddr,
		log:                 cfg.Logger,
		firstMessageTimeout: cfg.firstMessageTimeout,
	}
	if b.firstMessageTimeout == 0 {
		b.firstMessageTimeout = 10 * time.Second
	}
	if len(cfg.Servers) == 0 {
		return nil, errors.New("limaquic: no server names")
	}
	for _, name := range cfg.Servers {
		if _, err := limaegress.ParseName(string(name)); err != nil {
			return nil, fmt.Errorf("limaquic: server name: %w", err)
		}
		b.servers[string(name)] = true
	}
	for _, e := range cfg.Endpoints {
		if _, err := limaegress.ParseEndpoint(e.String()); err != nil {
			return nil, fmt.Errorf("limaquic: %w", err)
		}
		b.endpoints[e.String()] = true
		b.hosts[e.Host] = true
	}
	if b.port == 0 {
		b.port = 443
	}
	var err error
	if b.leaves, err = newLeaves(cfg.CA); err != nil {
		return nil, err
	}
	if b.resolver == nil {
		b.resolver = net.DefaultResolver
	}
	if b.dialPort == 0 {
		b.dialPort = 443
	}
	if b.allowAddr == nil {
		local, err := limaegress.InterfaceAddrs()
		if err != nil {
			return nil, err
		}
		b.allowAddr = upstreamCheck(local)
	}
	if b.log == nil {
		b.log = slog.New(slog.DiscardHandler)
	}
	return b, nil
}

// upstreamCheck reports whether an upstream address may be dialled, by the
// CONNECT relay's check: it is public, and none of local, the host's own
// addresses.
func upstreamCheck(local []netip.Addr) func(netip.Addr) bool {
	return func(a netip.Addr) bool {
		_, err := limaegress.CheckAddrs([]netip.Addr{a}, local)
		return err == nil
	}
}

// The flow's addresses, which quic-go needs and nothing reads: the flow has
// one peer, the guest's client behind the shim.
var (
	flowLocal = &net.UDPAddr{IP: net.IPv4(127, 0, 1, 1), Port: 443}
	flowPeer  = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
)

// Serve terminates the guest's QUIC connection carried by flow and relays
// its requests, until the flow ends, when it returns nil, or ctx ends.
// Every upstream request from the flow shares one HTTP/3 transport, so the
// guest's session and its dials reach the server over one connection.
func (b *Broker) Serve(ctx context.Context, flow io.ReadWriter) error {
	pc := udpframe.NewPacketConn(flow, flowLocal, flowPeer)
	defer pc.Close()
	f := &flowRelay{broker: b}
	f.transport = &http3.Transport{
		TLSClientConfig: &tls.Config{RootCAs: b.rootCAs, MinVersion: tls.VersionTLS13},
		QUICConfig:      &quic.Config{KeepAlivePeriod: 15 * time.Second},
		Dial:            f.dial,
	}
	defer f.transport.Close()
	// quic.Listen, unlike ListenEarly, accepts no 0-RTT data.
	ln, err := quic.Listen(pc, http3.ConfigureTLSConfig(&tls.Config{
		MinVersion:             tls.VersionTLS13,
		SessionTicketsDisabled: true,
		GetCertificate:         b.certificate,
	}), &quic.Config{
		Allow0RTT:             false,
		MaxIncomingStreams:    maxStreams,
		MaxIncomingUniStreams: maxUniStreams,
	})
	if err != nil {
		return err
	}
	defer ln.Close()
	srv := &http3.Server{Handler: f, Logger: b.log}
	stop := context.AfterFunc(ctx, func() {
		srv.Close()
		ln.Close()
	})
	defer stop()
	err = srv.ServeListener(&cappedListener{Listener: ln, slots: make(chan struct{}, maxConns), log: b.log})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// The bounds on what one flow may hold open at once: QUIC connections, and
// each connection's request streams and unidirectional streams, of which
// HTTP/3 needs three.
const (
	maxConns      = 4
	maxStreams    = 16
	maxUniStreams = 4
)

// cappedListener accepts at most cap(slots) connections at once, closing
// any beyond them with HTTP/3's excessive-load code.
type cappedListener struct {
	*quic.Listener
	slots chan struct{}
	log   *slog.Logger
}

func (l *cappedListener) Accept(ctx context.Context) (*quic.Conn, error) {
	for {
		conn, err := l.Listener.Accept(ctx)
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			context.AfterFunc(conn.Context(), func() { <-l.slots })
			return conn, nil
		default:
			l.log.Warn("refused", "reason", "connections", "max", cap(l.slots))
			conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeExcessiveLoad), "too many connections")
		}
	}
}

var errServerName = errors.New("limaquic: server name not served")

func (b *Broker) certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if !b.servers[hello.ServerName] {
		b.log.Warn("refused", "reason", "server name", "sni", hello.ServerName)
		return nil, errServerName
	}
	return b.leaves.get(hello.ServerName)
}

// flowRelay serves one flow's requests over its own upstream transport.
type flowRelay struct {
	broker    *Broker
	transport *http3.Transport
}

// relayedHeaders are the only request headers copied upstream.
var relayedHeaders = []string{"Authorization", "Content-Type", "User-Agent"}

// paths maps each :path the broker serves to the constant it relays to.
var paths = map[string]string{pathSession: pathSession, pathDial: pathDial, pathGetHost: pathGetHost}

// refuse answers 403 and logs why, with attrs that name no credential.
func (f *flowRelay) refuse(w http.ResponseWriter, reason string, attrs ...any) {
	f.broker.log.Warn("refused", append([]any{"reason", reason}, attrs...)...)
	http.Error(w, "refused", http.StatusForbidden)
}

func (f *flowRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, log := f.broker, f.broker.log
	sni := r.TLS.ServerName
	if r.Method != http.MethodPost {
		f.refuse(w, "method", "method", r.Method)
		return
	}
	path, ok := paths[r.RequestURI]
	if !ok {
		f.refuse(w, "path", "path", r.RequestURI)
		return
	}
	if !authorityMatches(r.Host, sni, b.port) {
		f.refuse(w, "authority", "authority", r.Host)
		return
	}
	// Every path's body starts with one message, which the broker reads,
	// within firstMessageTimeout, judges, and relays ahead of the rest of
	// the body.
	body := http.NewResponseController(w)
	if err := body.SetReadDeadline(time.Now().Add(b.firstMessageTimeout)); err != nil {
		f.refuse(w, "first message", "path", path, "error", err)
		return
	}
	first, message, err := readFirst(r.Body)
	if err != nil {
		f.refuse(w, "first message", "path", path, "error", err)
		return
	}
	switch path {
	case pathSession:
		// After its SessionReq, a session carries control frames, which
		// name no destination, so they pass unread.
		if err := checkSessionReq(message); err != nil {
			f.refuse(w, "first message", "path", path, "error", err)
			return
		}
	case pathDial:
		host, port, err := parseDialReq(message)
		if err != nil {
			f.refuse(w, "first message", "path", path, "error", err)
			return
		}
		endpoint := net.JoinHostPort(host, strconv.FormatUint(port, 10))
		if !b.endpoints[endpoint] {
			f.refuse(w, "endpoint", "endpoint", endpoint)
			return
		}
		log.Info("dial", "endpoint", endpoint)
	case pathGetHost:
		host, err := parseGetHostReq(message)
		if err != nil {
			f.refuse(w, "first message", "path", path, "error", err)
			return
		}
		if !b.hosts[host] {
			f.refuse(w, "host", "host", host)
			return
		}
		// A get-host is one GetHostReq and nothing after it, so nothing
		// the broker has not judged reaches upstream.
		if err := atEnd(r.Body); err != nil {
			f.refuse(w, "first message", "path", path, "error", err)
			return
		}
	}
	if err := body.SetReadDeadline(time.Time{}); err != nil {
		f.refuse(w, "first message", "path", path, "error", err)
		return
	}

	target := &url.URL{Scheme: "https", Host: sni, Path: path}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), io.MultiReader(bytes.NewReader(first), r.Body))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, name := range relayedHeaders {
		if values, ok := r.Header[name]; ok {
			upstream.Header[name] = values
		}
	}
	resp, err := f.transport.RoundTrip(upstream)
	if err != nil {
		log.Warn("upstream failed", "path", path, "error", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	log.Info("relayed", "path", path, "status", resp.StatusCode)
	for name, values := range resp.Header {
		if name != "Trailer" {
			w.Header()[name] = values
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher := http.NewResponseController(w)
	flusher.Flush()
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			break
		}
	}
	for name, values := range resp.Trailer {
		w.Header()[http.TrailerPrefix+name] = values
	}
}

// dial connects to the flow's server for the transport: it resolves the
// name on the host, refuses the lot if any answer may not be dialled, and
// dials the first, verified as the name, with no session to resume.
func (f *flowRelay) dial(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
	b := f.broker
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if !b.servers[host] {
		return nil, fmt.Errorf("limaquic: upstream %q is not a server name", host)
	}
	answers, err := b.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("limaquic: no address for %s", host)
	}
	for _, a := range answers {
		if !b.allowAddr(a) {
			b.log.Warn("upstream refused", "host", host, "addr", a.String())
			return nil, fmt.Errorf("limaquic: %s resolves to %s, which may not be dialled", host, a)
		}
	}
	pc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	tlsCfg = tlsCfg.Clone()
	tlsCfg.ServerName = host
	tlsCfg.ClientSessionCache = nil
	target := net.UDPAddrFromAddrPort(netip.AddrPortFrom(answers[0].Unmap(), b.dialPort))
	conn, err := quic.Dial(ctx, pc, target, tlsCfg, cfg)
	if err != nil {
		pc.Close()
		return nil, err
	}
	context.AfterFunc(conn.Context(), func() { pc.Close() })
	return conn, nil
}
