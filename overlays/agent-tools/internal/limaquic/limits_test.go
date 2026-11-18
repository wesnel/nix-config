package limaquic

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// quicDialer dials QUIC connections to the shim from one UDP socket, so
// that every connection it makes shares one flow.
func (f *fixture) quicDialer(t *testing.T) func(context.Context) *quic.Conn {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: pc}
	t.Cleanup(func() {
		tr.Close()
		pc.Close()
	})
	shim := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(f.shimPort)}
	tlsConf := &tls.Config{RootCAs: f.brokerCA.pool, ServerName: server, NextProtos: []string{http3.NextProtoH3}}
	return func(ctx context.Context) *quic.Conn {
		t.Helper()
		conn, err := tr.Dial(ctx, shim, tlsConf, &quic.Config{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.CloseWithError(0, "") })
		return conn
	}
}

func TestLimits(t *testing.T) {
	t.Run("a flow carries at most four connections", func(t *testing.T) {
		f := newFixture(t, nil)
		ctx := testContext(t)
		dial := f.quicDialer(t)
		getHost := []byte{13, 0x0a, 11, 'd', 'b', '.', 'i', 'n', 't', 'e', 'r', 'n', 'a', 'l'}
		var h3 http3.Transport
		for i := range 4 {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://%s:%d/get-host", server, f.shimPort), bytes.NewReader(getHost))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := h3.NewClientConn(dial(ctx)).RoundTrip(req)
			if err != nil {
				t.Fatalf("connection %d: %v", i+1, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("connection %d: status %d, want 200", i+1, resp.StatusCode)
			}
		}
		fifth := dial(ctx)
		select {
		case <-fifth.Context().Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the flow's fifth connection stayed open")
		}
		var closed *quic.ApplicationError
		if err := context.Cause(fifth.Context()); !errors.As(err, &closed) || !closed.Remote || closed.ErrorCode != quic.ApplicationErrorCode(http3.ErrCodeExcessiveLoad) {
			t.Fatalf("the fifth connection ended with %v, want the broker's excessive-load close", err)
		}
		if !f.logs.has("refused", map[string]string{"reason": "connections"}) {
			t.Fatal("no refusal of the fifth connection")
		}
		if flows := len(f.flows); flows != 1 {
			t.Fatalf("the five connections took %d flows, want 1", flows)
		}
	})

	t.Run("a connection opens at most sixteen streams at once", func(t *testing.T) {
		f := newFixture(t, nil)
		conn := f.quicDialer(t)(testContext(t))
		for i := range 16 {
			if _, err := conn.OpenStream(); err != nil {
				t.Fatalf("stream %d: %v", i+1, err)
			}
		}
		var limit *quic.StreamLimitReachedError
		if _, err := conn.OpenStream(); !errors.As(err, &limit) {
			t.Fatalf("the 17th stream opened (%v), want the stream limit", err)
		}
	})

	t.Run("a connection opens at most four unidirectional streams at once", func(t *testing.T) {
		f := newFixture(t, nil)
		conn := f.quicDialer(t)(testContext(t))
		for i := range 4 {
			if _, err := conn.OpenUniStream(); err != nil {
				t.Fatalf("unidirectional stream %d: %v", i+1, err)
			}
		}
		var limit *quic.StreamLimitReachedError
		if _, err := conn.OpenUniStream(); !errors.As(err, &limit) {
			t.Fatalf("the fifth unidirectional stream opened (%v), want the stream limit", err)
		}
	})
}
