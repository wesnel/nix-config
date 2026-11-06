package limaegress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"
)

// Stream is one end of a relayed connection: bytes both ways, and a way to
// say that no more will be written while still reading. *net.TCPConn is
// one.
type Stream interface {
	io.Reader
	io.Writer
	CloseWrite() error
}

// Upstream is a connection the relay opened to a checked address.
type Upstream interface {
	Stream
	io.Closer
}

const (
	// maxHeader caps the CONNECT request, request line and headers with
	// their terminating blank line.
	maxHeader = 8 << 10
	// headerTimeout is how long the guest has to send the whole request.
	headerTimeout = 10 * time.Second
	// port is the only port the guest may ask for.
	port = 443
)

// Relay serves guest connections as an HTTPS CONNECT proxy. Its zero
// fields but Policy have defaults.
type Relay struct {
	// Policy says which hosts the guest may reach. It is required.
	Policy *Policy
	// Resolve looks a host up on the host. It is called once per
	// connection, and only the addresses it returns are dialed. Nil uses
	// net.DefaultResolver.
	Resolve func(ctx context.Context, host Name) ([]netip.Addr, error)
	// Dial opens a TCP connection to one checked address. Nil uses a
	// net.Dialer with a 10 s timeout.
	Dial func(ctx context.Context, addr netip.AddrPort) (Upstream, error)
	// LocalAddrs returns the host's own addresses, which the guest may not
	// reach. Nil reads the host's interfaces; an error refuses the
	// connection.
	LocalAddrs func() ([]netip.Addr, error)
	// Slots caps the relays running at once. It is required: with none,
	// or none free, Serve refuses at once.
	Slots Slots
	// Log receives one line per connection. Nil discards it.
	Log io.Writer

	// after is time.After, and a clock of the test's in tests.
	after func(time.Duration) <-chan time.Time
}

// refusal is a verdict against the guest's request, answered with 403.
type refusal struct{ reason string }

func (r refusal) Error() string { return r.reason }

func refuse(format string, args ...any) error {
	return refusal{reason: fmt.Sprintf(format, args...)}
}

// errTimeout is the guest taking too long for a step that has a deadline.
var errTimeout = errors.New("timed out")

// verdict is what the log line says of one connection.
type verdict struct {
	host     string
	port     int
	up, down atomic.Int64
}

// Serve relays one guest connection: it reads one CONNECT request, checks
// it, connects to the host and splices the two connections, propagating
// each side's half-close to the other. It answers a refused request with
// 403 and a failure to connect with 502, then half-closes the guest. It
// returns nil once a relayed connection ends cleanly, and otherwise why it
// did not. A goroutine may still be reading guest when it returns: the
// caller closes guest afterwards.
func (r *Relay) Serve(ctx context.Context, guest Stream) error {
	var v verdict
	err := r.serve(ctx, guest, &v)
	r.logVerdict(&v, err)
	return err
}

func (r *Relay) serve(ctx context.Context, guest Stream, v *verdict) error {
	if r.Slots == nil {
		return answer(guest, refuse("no relay slots are configured"))
	}
	release, err := r.Slots.Acquire()
	if errors.Is(err, ErrNoSlot) {
		return answer(guest, refuse("%v", err))
	}
	if err != nil {
		return answer(guest, err)
	}
	defer release()
	br := bufio.NewReader(guest)
	target, err := within(r.after, headerTimeout, func() (string, error) { return readTarget(br) })
	if err != nil {
		return answer(guest, err)
	}
	host, err := r.checkTarget(target, v)
	if err != nil {
		return answer(guest, err)
	}
	up, err := r.connect(ctx, host)
	if err != nil {
		return answer(guest, err)
	}
	defer up.Close()
	if _, err := io.WriteString(guest, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return err
	}
	hello, err := within(r.after, helloTimeout, func() ([]byte, error) { return checkHello(br, host) })
	if err != nil {
		guest.CloseWrite()
		return err
	}
	return splice(guest, br, up, hello, v)
}

// readTarget reads one HTTP/1.1 CONNECT request of at most maxHeader
// bytes and returns its request target. Bytes after the request stay in br.
func readTarget(br *bufio.Reader) (string, error) {
	var header []byte
	for !bytes.HasSuffix(header, []byte("\n\r\n")) && !bytes.HasSuffix(header, []byte("\n\n")) {
		if len(header) == maxHeader {
			return "", refuse("request headers exceed %d bytes", maxHeader)
		}
		c, err := br.ReadByte()
		if err != nil {
			return "", refuse("reading the request: %v", err)
		}
		header = append(header, c)
	}
	// The reasons are fixed text: the log never holds the guest's.
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(header)))
	if err != nil {
		return "", refuse("a malformed request")
	}
	if req.Method != http.MethodConnect {
		return "", refuse("the method is not CONNECT")
	}
	if req.Proto != "HTTP/1.1" {
		return "", refuse("the protocol is not HTTP/1.1")
	}
	if req.ContentLength != 0 || len(req.TransferEncoding) != 0 {
		return "", refuse("the request has a body")
	}
	return req.RequestURI, nil
}

// maxLoggedHost caps the unparsed host or target the log line holds.
const maxLoggedHost = 255

func capLogged(s string) string {
	if len(s) > maxLoggedHost {
		return s[:maxLoggedHost]
	}
	return s
}

// checkTarget parses a request target as host:443 and checks the host
// against the policy, recording what it parsed in v. Its reasons are fixed
// text, and v holds at most maxLoggedHost bytes of the guest's.
func (r *Relay) checkTarget(target string, v *verdict) (Name, error) {
	v.host = capLogged(target)
	h, p, err := net.SplitHostPort(target)
	if err != nil {
		return "", refuse("the target is not host:port")
	}
	v.host = capLogged(h)
	if n, err := strconv.Atoi(p); err == nil {
		v.port = n
	}
	if p != strconv.Itoa(port) {
		return "", refuse("the port is not %d", port)
	}
	name, err := ParseName(h)
	if err != nil {
		return "", refuse("the host is not a valid name")
	}
	v.host = string(name)
	if !r.Policy.Allows(name) {
		return "", refuse("host %q is not allowed", name)
	}
	return name, nil
}

// connect resolves host once, refuses it unless every address is public
// and not the host's own, and dials those checked addresses in turn.
func (r *Relay) connect(ctx context.Context, host Name) (Upstream, error) {
	localAddrs := r.LocalAddrs
	if localAddrs == nil {
		localAddrs = InterfaceAddrs
	}
	local, err := localAddrs()
	if err != nil {
		return nil, refuse("reading the host's addresses: %v", err)
	}
	resolve := r.Resolve
	if resolve == nil {
		resolve = lookupFQDN(net.DefaultResolver.LookupNetIP)
	}
	addrs, err := resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolving %q: no addresses", host)
	}
	addrs, err = CheckAddrs(addrs, local)
	if err != nil {
		return nil, err
	}
	dial := r.Dial
	if dial == nil {
		dial = defaultDial
	}
	var errs []error
	for _, addr := range addrs {
		up, err := dial(ctx, netip.AddrPortFrom(addr, port))
		if err == nil {
			return up, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("connecting to %q: %w", host, errors.Join(errs...))
}

// lookupFQDN returns a resolver that looks a host up through lookup as a
// fully qualified name, with its trailing dot, so that no search domain of
// the host's resolver configuration turns it into another name.
func lookupFQDN(lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)) func(context.Context, Name) ([]netip.Addr, error) {
	return func(ctx context.Context, host Name) ([]netip.Addr, error) {
		return lookup(ctx, "ip", string(host)+".")
	}
}

func defaultDial(ctx context.Context, addr netip.AddrPort) (Upstream, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

// answer tells the guest why the relay will not connect, half-closes it,
// and returns err.
func answer(guest Stream, err error) error {
	status := "502 Bad Gateway"
	if errors.As(err, new(refusal)) || errors.Is(err, errTimeout) {
		status = "403 Forbidden"
	}
	io.WriteString(guest, "HTTP/1.1 "+status+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
	guest.CloseWrite()
	return err
}

// splice copies first and then the rest of guestR to up, and up to guest,
// each direction half-closing its destination at its source's end. It
// returns once both directions have ended, or once either fails, closing
// up.
func splice(guest Stream, guestR io.Reader, up Upstream, first []byte, v *verdict) error {
	done := make(chan error, 2)
	go func() {
		err := copyCounted(up, io.MultiReader(bytes.NewReader(first), guestR), &v.up)
		if err == nil {
			err = up.CloseWrite()
		}
		done <- err
	}()
	go func() {
		err := copyCounted(guest, up, &v.down)
		if err == nil {
			err = guest.CloseWrite()
		}
		done <- err
	}()
	for range 2 {
		if err := <-done; err != nil {
			up.Close()
			return err
		}
	}
	return nil
}

func copyCounted(dst io.Writer, src io.Reader, n *atomic.Int64) error {
	_, err := io.Copy(countingWriter{dst, n}, src)
	return err
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// within runs f and returns its result, or errTimeout once d has passed on
// after's clock. On a timeout, f is left running.
func within[T any](after func(time.Duration) <-chan time.Time, d time.Duration, f func() (T, error)) (T, error) {
	if after == nil {
		after = time.After
	}
	type result struct {
		v   T
		err error
	}
	c := make(chan result, 1)
	go func() {
		v, err := f()
		c <- result{v, err}
	}()
	select {
	case res := <-c:
		return res.v, res.err
	case <-after(d):
		var zero T
		return zero, errTimeout
	}
}

// logVerdict writes the connection's one log line: never payload.
func (r *Relay) logVerdict(v *verdict, err error) {
	if r.Log == nil {
		return
	}
	outcome, reason := "relayed", ""
	switch {
	case err == nil:
	case errors.As(err, new(refusal)) || errors.Is(err, errTimeout):
		outcome, reason = "refused", err.Error()
	default:
		outcome, reason = "failed", err.Error()
	}
	fmt.Fprintf(r.Log, "lima-egress connect: host=%q port=%d verdict=%s reason=%q up=%d down=%d\n",
		v.host, v.port, outcome, reason, v.up.Load(), v.down.Load())
}
