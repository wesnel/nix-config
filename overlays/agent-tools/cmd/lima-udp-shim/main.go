// Command lima-udp-shim runs in the Lima guest. It listens for UDP
// datagrams, and for each client source address opens one TCP connection to
// the host's QUIC broker, through QEMU's guestfwd, which carries only TCP.
// It carries that client's datagrams over the connection both ways, framed
// as internal/udpframe says. The guest is untrusted, so it holds no policy:
// the broker decides what passes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/udpframe"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lima-udp-shim:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("lima-udp-shim", flag.ContinueOnError)
	listen := flags.String("listen", "", "UDP `address` to listen on, such as 127.0.1.1:443")
	connect := flags.String("connect", "", "TCP `address` of the host's broker, such as 192.168.5.101:443")
	idle := flags.Duration("idle", 5*time.Minute, "close a client's connection after this long with no datagram either way")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments %q", flags.Args())
	}
	if _, _, err := net.SplitHostPort(*connect); err != nil {
		return fmt.Errorf("-connect: %w", err)
	}
	if *idle <= 0 {
		return errors.New("-idle must be positive")
	}
	pc, err := net.ListenPacket("udp", *listen)
	if err != nil {
		return err
	}
	defer pc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var dialer net.Dialer
	shim := &udpframe.Shim{
		Dial:   func(ctx context.Context) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", *connect) },
		Idle:   *idle,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	if err := shim.Serve(ctx, pc); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
