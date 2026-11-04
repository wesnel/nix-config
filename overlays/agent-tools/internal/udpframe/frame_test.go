package udpframe

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

var (
	local = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
	peer  = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5000}
)

// pipeConn returns a PacketConn over one end of a pipe and the pipe's other
// end, where the test plays the stream's far side.
func pipeConn(t *testing.T) (*PacketConn, net.Conn) {
	t.Helper()
	near, far := net.Pipe()
	// A bound, not a wait: a frame that never comes fails the test.
	far.SetDeadline(time.Now().Add(10 * time.Second))
	conn := NewPacketConn(near, local, peer)
	t.Cleanup(func() {
		conn.Close()
		near.Close()
		far.Close()
	})
	return conn, far
}

func TestPacketConn(t *testing.T) {
	t.Run("a datagram written goes out as one length-prefixed frame", func(t *testing.T) {
		conn, far := pipeConn(t)
		written := make(chan error, 1)
		go func() {
			_, err := conn.WriteTo([]byte("hi"), peer)
			written <- err
		}()
		got := make([]byte, 4)
		if _, err := io.ReadFull(far, got); err != nil {
			t.Fatal(err)
		}
		if want := []byte{0, 2, 'h', 'i'}; !bytes.Equal(got, want) {
			t.Fatalf("frame %v, want %v", got, want)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("frames read back as datagrams from the fixed peer, an empty one included", func(t *testing.T) {
		conn, far := pipeConn(t)
		go far.Write([]byte{0, 3, 'a', 'b', 'c', 0, 0, 0, 1, 'z'})
		for _, want := range []string{"abc", "", "z"} {
			buf := make([]byte, 64)
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				t.Fatal(err)
			}
			if string(buf[:n]) != want {
				t.Fatalf("datagram %q, want %q", buf[:n], want)
			}
			if from.String() != "127.0.0.2:5000" {
				t.Fatalf("from %v, want 127.0.0.2:5000", from)
			}
		}
		if got := conn.LocalAddr().String(); got != "127.0.0.1:443" {
			t.Fatalf("local %v, want 127.0.0.1:443", got)
		}
	})

	t.Run("a datagram longer than the buffer is truncated, as UDP's are", func(t *testing.T) {
		conn, far := pipeConn(t)
		go far.Write([]byte{0, 4, 'a', 'b', 'c', 'd'})
		buf := make([]byte, 2)
		n, _, err := conn.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "ab" {
			t.Fatalf("ReadFrom = %q, %v; want \"ab\", nil", buf[:n], err)
		}
	})

	t.Run("a datagram too long for a frame is refused", func(t *testing.T) {
		conn, _ := pipeConn(t)
		if _, err := conn.WriteTo(make([]byte, 65536), peer); err == nil {
			t.Fatal("a 65536-byte datagram was written")
		}
	})

	t.Run("the stream's end ends reads", func(t *testing.T) {
		conn, far := pipeConn(t)
		go func() {
			far.Write([]byte{0, 1, 'x'})
			far.Close()
		}()
		buf := make([]byte, 8)
		if _, _, err := conn.ReadFrom(buf); err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.ReadFrom(buf); !errors.Is(err, io.EOF) {
			t.Fatalf("ReadFrom after the stream ended: %v, want EOF", err)
		}
	})

	t.Run("a frame cut short is an error", func(t *testing.T) {
		conn, far := pipeConn(t)
		go func() {
			far.Write([]byte{0, 5, 'x'})
			far.Close()
		}()
		if _, _, err := conn.ReadFrom(make([]byte, 8)); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("ReadFrom of a short frame: %v, want unexpected EOF", err)
		}
	})

	t.Run("close unblocks a waiting read", func(t *testing.T) {
		conn, _ := pipeConn(t)
		done := make(chan error, 1)
		go func() {
			_, _, err := conn.ReadFrom(make([]byte, 8))
			done <- err
		}()
		conn.Close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("ReadFrom after Close: %v, want net.ErrClosed", err)
		}
		if _, err := conn.WriteTo([]byte("x"), peer); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("WriteTo after Close: %v, want net.ErrClosed", err)
		}
	})

	t.Run("a passed read deadline times reads out", func(t *testing.T) {
		conn, far := pipeConn(t)
		if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		_, _, err := conn.ReadFrom(make([]byte, 8))
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("ReadFrom past its deadline: %v, want a timeout", err)
		}
		// Clearing the deadline lets the next datagram through.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		go far.Write([]byte{0, 1, 'y'})
		buf := make([]byte, 8)
		n, _, err := conn.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "y" {
			t.Fatalf("ReadFrom after clearing the deadline = %q, %v", buf[:n], err)
		}
	})
}
