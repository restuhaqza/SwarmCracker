// Package main implements sc-agent, the guest-side agent injected into every
// SwarmCracker microVM.
//
// sc-agent listens on an AF_VSOCK port inside the guest and serves the
// pkg/agent protocol to the host, which reaches it through Firecracker's vsock
// Unix-socket proxy. It is a Linux-only runtime binary; the non-linux Listen
// stub exists solely so the tree keeps building on development hosts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// acceptErrorBackoff throttles the accept loop after a non-fatal error so a
// persistent failure (for example file-descriptor exhaustion) cannot spin the
// CPU.
const acceptErrorBackoff = 10 * time.Millisecond

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args, starts the listener and serves connections until the
// listener is closed. It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	if opts.version {
		fmt.Fprintln(stdout, agent.Version)
		return 0
	}

	log.Logger = zerolog.New(stderr).With().Timestamp().Logger()

	ln, err := Listen(opts.port)
	if err != nil {
		fmt.Fprintf(stderr, "sc-agent: %v\n", err)
		return 1
	}
	defer func() { _ = ln.Close() }()

	log.Info().Uint32("port", opts.port).Msg("sc-agent listening on vsock")
	return serve(ln)
}

// options holds the parsed command-line options.
type options struct {
	port    uint32
	version bool
}

// parseFlags parses sc-agent's flags. A parse failure has already been reported
// to stderr by the flag package.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("sc-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Uint("port", uint(agent.DefaultPort), "AF_VSOCK port to listen on")
	version := fs.Bool("version", false, "print the agent protocol version and exit")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if *port > math.MaxUint32 {
		fmt.Fprintf(stderr, "sc-agent: -port %d out of range (maximum %d)\n", *port, uint64(math.MaxUint32))
		return options{}, fmt.Errorf("port %d out of range", *port)
	}
	// Safe: *port is bounded by the check above.
	return options{port: uint32(*port), version: *version}, nil //nolint:gosec // G115: range-checked
}

// serve accepts connections until ln is closed, handing each to agent.ServeConn
// in its own goroutine. A transient Accept error is logged and retried; serve
// returns 0 only once the listener is closed.
func serve(ln net.Listener) int {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return 0
			}
			log.Error().Err(err).Msg("accept failed")
			time.Sleep(acceptErrorBackoff)
			continue
		}
		go handleConn(conn)
	}
}

// handleConn serves a single guest connection and closes it afterwards.
func handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := agent.ServeConn(conn); err != nil {
		remote := ""
		if addr := conn.RemoteAddr(); addr != nil {
			remote = addr.String()
		}
		log.Error().Err(err).Str("remote", remote).Msg("serve connection failed")
	}
}
