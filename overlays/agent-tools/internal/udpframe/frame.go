// Package udpframe carries UDP datagrams over a byte stream, such as a TCP
// connection: each datagram is a frame of a big-endian uint16 length
// followed by that many bytes, in both directions. The Lima guest's UDP
// shim and the host's HTTP/3 broker both speak it.
package udpframe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// MaxDatagram is the longest datagram a frame holds.
const MaxDatagram = 0xffff

// Write writes datagram to w as one frame, in a single Write call.
func Write(w io.Writer, datagram []byte) error {
	if len(datagram) > MaxDatagram {
		return fmt.Errorf("udpframe: a %d-byte datagram exceeds %d bytes", len(datagram), MaxDatagram)
	}
	frame := make([]byte, 2+len(datagram))
	binary.BigEndian.PutUint16(frame, uint16(len(datagram))) //nolint:gosec // bounded by MaxDatagram above
	copy(frame[2:], datagram)
	_, err := w.Write(frame)
	return err
}

// Read reads one frame from r and returns its datagram. It returns io.EOF
// at the stream's end between frames, and io.ErrUnexpectedEOF inside one.
func Read(r io.Reader) ([]byte, error) {
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return nil, err
	}
	datagram := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(r, datagram); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return datagram, nil
}

// PacketConn presents one framed stream as a net.PacketConn whose every
// datagram comes from, and goes to, one fixed peer. A goroutine reads the
// stream from the start until it ends or fails; Close does not close the
// stream, which stays its owner's, so that goroutine ends only then.
type PacketConn struct {
	stream      io.Writer
	local, peer net.Addr

	datagrams chan []byte
	readErr   error // set before datagrams is closed

	writeMu sync.Mutex

	closeOnce sync.Once
	closed    chan struct{}

	deadlineMu sync.Mutex
	deadline   time.Time
	// deadlineSet is closed and replaced whenever the deadline changes,
	// which wakes a read waiting on the old one.
	deadlineSet chan struct{}
}

// NewPacketConn starts reading frames from stream and returns the
// PacketConn that serves them.
func NewPacketConn(stream io.ReadWriter, local, peer net.Addr) *PacketConn {
	c := &PacketConn{
		stream:      stream,
		local:       local,
		peer:        peer,
		datagrams:   make(chan []byte),
		closed:      make(chan struct{}),
		deadlineSet: make(chan struct{}),
	}
	go c.readFrames(stream)
	return c
}

func (c *PacketConn) readFrames(r io.Reader) {
	defer close(c.datagrams)
	for {
		datagram, err := Read(r)
		if err != nil {
			c.readErr = err
			return
		}
		select {
		case c.datagrams <- datagram:
		case <-c.closed:
			c.readErr = net.ErrClosed
			return
		}
	}
}

// ReadFrom returns the next datagram, truncated to p as a UDP read
// truncates, and the peer.
func (c *PacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, again, err := c.readOnce(p)
		if !again {
			if err != nil {
				return 0, nil, err
			}
			return n, c.peer, nil
		}
	}
}

// readOnce waits for a datagram under the current deadline, and asks to be
// called again when the deadline changes while it waits.
func (c *PacketConn) readOnce(p []byte) (n int, again bool, err error) {
	c.deadlineMu.Lock()
	deadline, changed := c.deadline, c.deadlineSet
	c.deadlineMu.Unlock()
	var expired <-chan time.Time
	if !deadline.IsZero() {
		wait := time.Until(deadline)
		if wait <= 0 {
			return 0, false, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-c.closed:
		return 0, false, net.ErrClosed
	case datagram, ok := <-c.datagrams:
		if !ok {
			return 0, false, c.readErr
		}
		return copy(p, datagram), false, nil
	case <-expired:
		return 0, false, os.ErrDeadlineExceeded
	case <-changed:
		return 0, true, nil
	}
}

// WriteTo writes p to the stream as one frame. The address is ignored: the
// stream has one peer.
func (c *PacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := Write(c.stream, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close ends reads and writes through c; it leaves the stream open.
func (c *PacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// LocalAddr returns the address c was made with.
func (c *PacketConn) LocalAddr() net.Addr { return c.local }

// SetDeadline sets the read deadline; writes have none.
func (c *PacketConn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetReadDeadline sets when waiting reads, and later ones, time out; the
// zero time clears it.
func (c *PacketConn) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	c.deadline = t
	close(c.deadlineSet)
	c.deadlineSet = make(chan struct{})
	return nil
}

// SetWriteDeadline does nothing: a write waits for the stream.
func (c *PacketConn) SetWriteDeadline(time.Time) error { return nil }
