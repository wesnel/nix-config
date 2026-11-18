package udpframe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"
)

// broker is the stream side the shim dials: it accepts connections, and on
// the n-th it answers each frame with "n:" and the datagram, so a reply
// names the connection that carried it. A connection whose number hangUp
// holds hangs up after its first reply.
type broker struct {
	ln      net.Listener
	hangUp  map[int]bool
	accepts chan int
	ends    chan int
}

func startBroker(t *testing.T, hangUp map[int]bool) *broker {
	t.Helper()
	b := &broker{hangUp: hangUp}
	return startBrokerWith(t, b, b.serve)
}

// startBrokerWith starts b's listener, serving each connection with handle
// in place of the echo.
func startBrokerWith(t *testing.T, b *broker, handle func(n int, conn net.Conn)) *broker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b.ln, b.accepts, b.ends = ln, make(chan int, 16), make(chan int, 16)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for n := 1; ; n++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.accepts <- n
			go handle(n, conn)
		}
	}()
	return b
}

func (b *broker) serve(n int, conn net.Conn) {
	defer func() { b.ends <- n }()
	defer conn.Close()
	for {
		datagram, err := Read(conn)
		if err != nil {
			return
		}
		if err := Write(conn, fmt.Appendf(nil, "%d:%s", n, datagram)); err != nil {
			return
		}
		if b.hangUp[n] {
			return
		}
	}
}

func (b *broker) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", b.ln.Addr().String())
}

// ended receives the client address of each "stream ended" line the shim
// logs, the signal that its next datagram dials a new stream.
type ended chan string

func (e ended) Enabled(context.Context, slog.Level) bool { return true }
func (e ended) WithAttrs([]slog.Attr) slog.Handler       { return e }
func (e ended) WithGroup(string) slog.Handler            { return e }
func (e ended) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "stream ended" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "client" {
				e <- a.Value.String()
			}
			return true
		})
	}
	return nil
}

func startShim(t *testing.T, b *broker, idle time.Duration) (net.Addr, ended) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ends := make(ended, 16)
	shim := &Shim{Dial: b.dial, Idle: idle, Logger: slog.New(ends)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- shim.Serve(ctx, pc) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Serve returned %v, want context.Canceled", err)
		}
		pc.Close()
	})
	return pc.LocalAddr(), ends
}

func waitEnded(t *testing.T, ends ended, c net.Conn) {
	t.Helper()
	select {
	case client := <-ends:
		if client != c.LocalAddr().String() {
			t.Fatalf("the stream of %s ended, want %s", client, c.LocalAddr())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no stream ended")
	}
}

func client(t *testing.T, shim net.Addr) net.Conn {
	t.Helper()
	conn, err := net.Dial("udp", shim.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// exchange sends datagram from c and returns the reply.
func exchange(t *testing.T, c net.Conn, datagram string) string {
	t.Helper()
	if _, err := c.Write([]byte(datagram)); err != nil {
		t.Fatal(err)
	}
	// A bound, not a wait: a reply that never comes fails the test.
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("reply to %q: %v", datagram, err)
	}
	return string(buf[:n])
}

func TestShim(t *testing.T) {
	t.Run("each client address gets its own stream, carried both ways", func(t *testing.T) {
		b := startBroker(t, nil)
		shim, _ := startShim(t, b, time.Minute)
		first, second := client(t, shim), client(t, shim)
		if got := exchange(t, first, "a1"); got != "1:a1" {
			t.Fatalf("first client's reply %q, want \"1:a1\"", got)
		}
		if got := exchange(t, second, "b1"); got != "2:b1" {
			t.Fatalf("second client's reply %q, want \"2:b1\"", got)
		}
		if got := exchange(t, first, "a2"); got != "1:a2" {
			t.Fatalf("first client's second reply %q, want \"1:a2\" on the same stream", got)
		}
		if got := exchange(t, second, ""); got != "2:" {
			t.Fatalf("second client's empty datagram got %q, want \"2:\"", got)
		}
		// Each accept is counted before its stream replies, so every
		// stream the exchanges used is counted by now.
		if accepted := len(b.accepts); accepted != 2 {
			t.Fatalf("%d streams for two clients, want 2", accepted)
		}
	})

	t.Run("a stream the far side ends is dialled again for the next datagram", func(t *testing.T) {
		b := startBroker(t, map[int]bool{1: true})
		shim, ends := startShim(t, b, time.Minute)
		c := client(t, shim)
		if got := exchange(t, c, "x"); got != "1:x" {
			t.Fatalf("reply %q, want \"1:x\"", got)
		}
		waitEnded(t, ends, c)
		if got := exchange(t, c, "y"); got != "2:y" {
			t.Fatalf("reply after the stream ended %q, want \"2:y\"", got)
		}
	})

	t.Run("an idle stream is closed, and dialled again for the next datagram", func(t *testing.T) {
		b := startBroker(t, nil)
		shim, ends := startShim(t, b, 50*time.Millisecond)
		c := client(t, shim)
		if got := exchange(t, c, "x"); got != "1:x" {
			t.Fatalf("reply %q, want \"1:x\"", got)
		}
		waitEnded(t, ends, c)
		select {
		case n := <-b.ends:
			if n != 1 {
				t.Fatalf("stream %d ended, want 1", n)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the broker's stream did not end")
		}
		if got := exchange(t, c, "y"); got != "2:y" {
			t.Fatalf("reply after idling %q, want \"2:y\"", got)
		}
	})
}

// The traffic of the idle tests: a datagram every tick for three idle
// periods, so a stream whose idle timer traffic does not reset ends
// partway, and one whose timer it resets never comes within six ticks of
// idling.
const (
	idle    = time.Second
	tick    = idle / 10
	traffic = 3 * idle
)

func TestShimIdle(t *testing.T) {
	t.Run("datagrams to the far side alone keep a stream open", func(t *testing.T) {
		t.Parallel()
		received := make(chan string, 64)
		b := startBrokerWith(t, &broker{}, func(_ int, conn net.Conn) {
			defer conn.Close()
			for {
				datagram, err := Read(conn)
				if err != nil {
					return
				}
				received <- string(datagram)
			}
		})
		shim, ends := startShim(t, b, idle)
		c := client(t, shim)
		sent := 0
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for deadline := time.Now().Add(traffic); time.Now().Before(deadline); <-ticker.C {
			if _, err := c.Write(fmt.Appendf(nil, "%d", sent)); err != nil {
				t.Fatal(err)
			}
			sent++
		}
		last := fmt.Sprint(sent - 1)
		for got := ""; got != last; {
			select {
			case got = <-received:
			case <-time.After(10 * time.Second):
				t.Fatalf("the far side's last datagram was %q, want %q", got, last)
			}
		}
		if len(ends) != 0 || len(b.accepts) != 1 {
			t.Fatalf("%d streams ended and %d were dialled; want one stream, still open", len(ends), len(b.accepts))
		}
	})

	t.Run("frames from the far side alone keep a stream open", func(t *testing.T) {
		t.Parallel()
		pushed := make(chan int, 1)
		// The far side keeps its connection open until the test has
		// asserted: its close would end the stream, rightly.
		asserted := make(chan struct{})
		b := startBrokerWith(t, &broker{}, func(_ int, conn net.Conn) {
			defer conn.Close()
			defer func() { <-asserted }()
			if _, err := Read(conn); err != nil {
				return
			}
			n := 0
			ticker := time.NewTicker(tick)
			defer ticker.Stop()
			for deadline := time.Now().Add(traffic); time.Now().Before(deadline); <-ticker.C {
				if err := Write(conn, fmt.Appendf(nil, "%d", n)); err != nil {
					break
				}
				n++
			}
			pushed <- n
		})
		shim, ends := startShim(t, b, idle)
		c := client(t, shim)
		if _, err := c.Write([]byte("open")); err != nil {
			t.Fatal(err)
		}
		var n int
		select {
		case n = <-pushed:
		case <-time.After(traffic + 10*time.Second):
			t.Fatal("the far side did not finish pushing")
		}
		// Every frame the far side wrote reaches the client, the last
		// one included.
		last := fmt.Sprint(n - 1)
		buf := make([]byte, 64)
		for got := ""; got != last; {
			c.SetReadDeadline(time.Now().Add(10 * time.Second))
			k, err := c.Read(buf)
			if err != nil {
				t.Fatalf("the client's last datagram was %q, want %q: %v", got, last, err)
			}
			got = string(buf[:k])
		}
		if len(ends) != 0 {
			t.Fatalf("the stream ended while frames came from the far side")
		}
		close(asserted)
	})
}
