package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"

	"golang.org/x/sys/unix"

	"github.com/wesnel/agent-tools/internal/limaegress"
	"github.com/wesnel/agent-tools/internal/limaquic"
	"github.com/wesnel/agent-tools/internal/limavm"
)

const (
	// connectSlots and quicSlots cap the CONNECT relays and QUIC flows
	// running at once; QEMU spawns one process for each guest connection.
	connectSlots = 64
	quicSlots    = 16
)

// egressMain runs `lima-mcp lima-egress connect|quic --config FILE`, as a
// guestfwd command. slirp gives the command one socket as its stdin,
// stdout and stderr, the guest's connection, so nothing but the relayed
// bytes may be written to any of them: the relay logs to the managed home's
// egress log, which stderr is pointed at before anything else can write to
// it.
func egressMain(args []string, guest *os.File) error {
	if len(args) == 0 || (args[0] != "connect" && args[0] != "quic") {
		return errors.New("usage: lima-mcp lima-egress connect|quic --config FILE")
	}
	mode := args[0]
	flags := flag.NewFlagSet("lima-egress "+mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configFile := flags.String("config", "", "the managed VM's configuration")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configFile == "" {
		return errors.New("usage: lima-mcp lima-egress connect|quic --config FILE")
	}
	c, err := limavm.LoadConfig(*configFile)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(c.EgressLog(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // in the Lima home of the store configuration, which only a switch writes
	if err != nil {
		return err
	}
	defer log.Close()
	if err := redirectStderr(log); err != nil {
		return err
	}
	conn, err := net.FileConn(guest)
	if err != nil {
		fmt.Fprintln(log, "lima-egress:", err)
		return err
	}
	defer conn.Close()
	if err := runEgress(context.Background(), mode, c, conn, log); err != nil {
		fmt.Fprintf(log, "lima-egress %s: %v\n", mode, err)
		return err
	}
	return nil
}

// redirectStderr points file descriptor 2 at log, so that what the Go
// runtime writes there, such as a panic, reaches the log and not the guest.
func redirectStderr(log *os.File) error {
	return unix.Dup2(int(log.Fd()), 2)
}

// runEgress serves one guest connection: a CONNECT request, or one QUIC
// flow framed by the guest's shim. It closes guest when done.
func runEgress(ctx context.Context, mode string, c *limavm.Config, guest net.Conn, log io.Writer) error {
	defer guest.Close()
	switch mode {
	case "connect":
		stream, ok := guest.(limaegress.Stream)
		if !ok {
			return fmt.Errorf("the guest connection %T cannot half-close", guest)
		}
		relay := &limaegress.Relay{
			Policy: c.Egress,
			Slots:  limaegress.FlockSlots{Dir: c.Slots(), N: connectSlots},
			Log:    log,
		}
		return relay.Serve(ctx, stream)
	case "quic":
		release, err := limaegress.FlockSlots{Dir: c.QUICSlots(), N: quicSlots}.Acquire()
		if err != nil {
			return err
		}
		defer release()
		ca, err := limavm.LoadCA(c.CACert(), c.CAKey())
		if err != nil {
			return err
		}
		servers := make([]limaegress.Name, len(c.QUICServers))
		for i, s := range c.QUICServers {
			servers[i] = limaegress.Name(s)
		}
		var endpoints []limaegress.Endpoint
		for _, s := range c.Endpoints {
			e, err := limaegress.ParseEndpoint(fmt.Sprint(s))
			if err != nil {
				return err
			}
			endpoints = append(endpoints, e)
		}
		broker, err := limaquic.New(limaquic.Config{
			Servers:   servers,
			Endpoints: endpoints,
			CA:        ca,
			Logger:    slog.New(slog.NewTextHandler(log, nil)),
		})
		if err != nil {
			return err
		}
		return broker.Serve(ctx, guest)
	}
	return fmt.Errorf("unknown egress mode %q", mode)
}
