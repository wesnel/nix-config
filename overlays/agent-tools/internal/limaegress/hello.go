package limaegress

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"slices"
	"time"
)

const (
	// maxHello caps the bytes peeked for the TLS ClientHello.
	maxHello = 16 << 10
	// helloTimeout is how long the guest has to send its ClientHello.
	helloTimeout = 10 * time.Second
	// extensionECH is the encrypted_client_hello extension's code point.
	extensionECH = 0xfe0d
)

// errHelloRead stops crypto/tls once it has parsed the ClientHello.
var errHelloRead = errors.New("ClientHello read")

// checkHello reads the guest's TLS ClientHello from r, at most maxHello
// bytes, and returns every byte it read, for forwarding before the rest.
// It refuses a ClientHello that does not parse, that carries no server
// name or one other than host, or that carries encrypted_client_hello,
// whose inner name the relay cannot see.
func checkHello(r io.Reader, host Name) ([]byte, error) {
	var seen bytes.Buffer
	conn := &helloConn{r: io.TeeReader(io.LimitReader(r, maxHello), &seen)}
	var info *tls.ClientHelloInfo
	// crypto/tls parses the ClientHello; the callback keeps what it
	// parsed and stops the handshake before any reply.
	err := tls.Server(conn, &tls.Config{
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			info = chi
			return nil, errHelloRead
		},
	}).Handshake()
	if info == nil {
		return nil, refuse("reading the TLS ClientHello: %v", err)
	}
	if slices.Contains(info.Extensions, extensionECH) {
		return nil, refuse("the ClientHello carries encrypted_client_hello")
	}
	if info.ServerName == "" {
		return nil, refuse("the ClientHello has no server name")
	}
	if sni, err := ParseName(info.ServerName); err != nil || sni != host {
		// Fixed text: the log never holds the guest's.
		return nil, refuse("the ClientHello's server name is not the CONNECT host")
	}
	return seen.Bytes(), nil
}

// helloConn is the net.Conn crypto/tls reads the ClientHello from. What it
// writes, an alert, is dropped: the guest never sees it.
type helloConn struct{ r io.Reader }

func (c *helloConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *helloConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *helloConn) Close() error                     { return nil }
func (c *helloConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *helloConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *helloConn) SetDeadline(time.Time) error      { return nil }
func (c *helloConn) SetReadDeadline(time.Time) error  { return nil }
func (c *helloConn) SetWriteDeadline(time.Time) error { return nil }
