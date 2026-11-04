package udpframe

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

// queued is how many datagrams a client's stream holds while it is being
// dialled or is slow; past it, datagrams are dropped, as UDP drops them.
const queued = 64

// Shim is the guest's UDP shim. For each client source address that sends
// to it, it dials one stream and carries that client's datagrams over it as
// frames, writing the frames that come back to the client. It holds no
// policy: what a stream may carry is the far side's to decide.
type Shim struct {
	// Dial opens a client's stream.
	Dial func(context.Context) (net.Conn, error)
	// Idle ends a stream no datagram has passed through, either way, for
	// this long; the client's next datagram dials a new one.
	Idle time.Duration
	// Logger gets a line for each stream's start and end, with the client's
	// address and never a datagram; nil logs nothing.
	Logger *slog.Logger
}

// Serve relays the datagrams that reach pc until ctx ends or pc fails, and
// closes every stream before it returns. A stream also ends when its far
// side ends it; the client's next datagram then dials a new one.
func (s *Shim) Serve(ctx context.Context, pc net.PacketConn) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { pc.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()

	var (
		mu      sync.Mutex
		streams = map[string]chan []byte{}
		wg      sync.WaitGroup
	)
	defer wg.Wait()

	buf := make([]byte, MaxDatagram)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		datagram := append([]byte(nil), buf[:n]...)
		key := from.String()
		mu.Lock()
		out, ok := streams[key]
		if !ok {
			out = make(chan []byte, queued)
			streams[key] = out
			logger.Info("stream started", "client", key)
			wg.Go(func() {
				err := s.carry(ctx, pc, from, out)
				mu.Lock()
				delete(streams, key)
				mu.Unlock()
				// Datagrams still queued for the ended stream are lost,
				// as UDP may lose them.
				logger.Info("stream ended", "client", key, "error", err)
			})
		}
		select {
		case out <- datagram:
		default:
		}
		mu.Unlock()
	}
}

// carry dials one client's stream and relays until either side ends it or
// it idles.
func (s *Shim) carry(ctx context.Context, pc net.PacketConn, client net.Addr, out <-chan []byte) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	conn, err := s.Dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	idle := time.AfterFunc(s.Idle, func() { cancel(errIdle) })
	defer idle.Stop()

	go func() {
		for {
			datagram, err := Read(conn)
			if err != nil {
				cancel(err)
				return
			}
			idle.Reset(s.Idle)
			if _, err := pc.WriteTo(datagram, client); err != nil {
				cancel(err)
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case datagram := <-out:
			idle.Reset(s.Idle)
			if err := Write(conn, datagram); err != nil {
				return err
			}
		}
	}
}

var errIdle = errors.New("idle")
