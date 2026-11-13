// sandboxd - the node's sandbox control plane.
//
// It owns the three operations a sandbox has — create, exec, remove — and the reaper
// that takes an idle one back, and it serves them on the v1 API surface, so a caller
// at a shell (swarmcracker sandbox ...) and a caller over HTTP use the same routes,
// the same tokens and the same errors.
//
// # Why a daemon
//
// Two of a sandbox's resources outlive the command that asked for it. The VM is a
// process, and it must not die with the shell that started it. The egress allowlist's
// endpoints are sockets this process holds inside the sandbox's namespace
// (pkg/sandbox/netpolicy): a sandbox created by a one-shot command would lose its
// egress the moment that command exited. So the process that creates a sandbox is the
// process that keeps running, and that is this one.
//
// # What it shares with the node
//
// The state directory (sandbox records, the instance registry, the API token store),
// the jailer's chroot base, the rootfs directory and the Firecracker socket directory
// are the ones the node's other daemons use, so a sandbox is resolvable by the same
// readers as a task (pkg/vminstance) and its tokens are the tokens the node's API
// already accepts. Like the manager's own API server, it needs the node's cluster
// material: the surface it serves is the shared v1 API, and the node's identity is
// how a client reaches it. The sandbox routes themselves never touch the cluster.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/urfave/cli/v2"

	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/sandboxd"
)

// Version is set by the build (-X main.Version=...).
var Version = "0.0.0-dev"

// Defaults. They are the node's own paths, the ones swarmd-firecracker and the CLI
// use, so a sandbox created here is found by everything else on the node.
const (
	defaultStateDir   = "/var/lib/swarmkit"
	defaultGoldenDir  = "/var/lib/firecracker/golden"
	defaultRootfsDir  = "/var/lib/firecracker/rootfs"
	defaultLogDir     = "/var/run/swarmcracker"
	defaultSocket     = "/var/run/swarmcracker/sandboxd.sock"
	defaultKernelPath = "/usr/share/firecracker/vmlinux"
	defaultJailerPath = "/usr/local/bin/jailer"
	defaultChrootDir  = "/var/lib/swarmcracker/jailer"
	defaultParentCgrp = "firecracker"
	defaultUIDFirst   = 100000
	defaultUIDSize    = 256
	defaultVCPUs      = 1
	// defaultVMMPidsMax bounds the threads a sandbox's VMM may create. It is not a
	// guest limit — the guest's processes are inside the microVM, not in host
	// cgroups — it is what keeps a wedged VMM from filling the node's pid space, and
	// it is what gives a sandbox a cgroup for its teardown to free.
	defaultVMMPidsMax  = 512
	defaultMemoryMB    = 512
	defaultReapEvery   = 30 * time.Second
	defaultReadyWindow = 90 * time.Second
)

func main() {
	app := cli.NewApp()
	app.Name = "sandboxd"
	app.Usage = "The node's sandbox control plane"
	app.Version = Version
	app.Flags = appFlags()
	app.Action = run

	if err := app.Run(os.Args); err != nil {
		log.Fatal().Err(err).Msg("sandboxd stopped")
	}
}

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "state-dir", Value: defaultStateDir, Usage: "Node state directory (sandbox records, instance registry, API tokens, cluster material)"},
		&cli.StringFlag{Name: "socket", Value: defaultSocket, Usage: "Unix socket the control plane listens on"},
		&cli.StringFlag{Name: "api-listen", Value: "", Usage: "Optional TCP address for the API, e.g. 127.0.0.1:4250 (token authenticated)"},
		&cli.StringFlag{Name: "api-token-file", Value: "", Usage: "Token store the API authenticates against (default <state-dir>/api/tokens.json)"},
		&cli.StringFlag{Name: "api-cert-dir", Value: "", Usage: "Directory holding tls.crt and tls.key for the TCP listener (default <state-dir>/api)"},
		&cli.StringFlag{Name: "golden-dir", Value: defaultGoldenDir, Usage: "Directory holding sealed templates"},
		&cli.StringFlag{Name: "kernel-path", Value: defaultKernelPath, Usage: "Kernel a template with no mapped profile boots"},
		&cli.StringSliceFlag{Name: "kernel-profile", Usage: "Override a kernel profile as <profile>=<path> (repeatable)"},
		&cli.StringFlag{Name: "rootfs-dir", Value: defaultRootfsDir, Usage: "Base directory for per-sandbox disks (<rootfs-dir>/sandboxes/<id>.ext4)"},
		&cli.StringFlag{Name: "log-dir", Value: defaultLogDir, Usage: "Directory for sandbox console logs"},
		&cli.StringFlag{Name: "jailer-path", Value: defaultJailerPath, Usage: "Path to the jailer binary"},
		&cli.StringFlag{Name: "jailer-chroot-dir", Value: defaultChrootDir, Usage: "Base directory for jailer chroots"},
		&cli.StringFlag{Name: "jailer-uid-first", Value: fmt.Sprint(defaultUIDFirst), Usage: "First uid a jailed sandbox VMM may run as"},
		&cli.StringFlag{Name: "jailer-uid-size", Value: fmt.Sprint(defaultUIDSize), Usage: "Number of uids in the jail range"},
		&cli.StringFlag{Name: "parent-cgroup", Value: defaultParentCgrp, Usage: "Parent cgroup the jailer creates a sandbox's cgroup under"},
		&cli.IntFlag{Name: "vmm-pids-max", Value: defaultVMMPidsMax, Usage: "pids.max for a sandbox's VMM cgroup (0 = no cgroup)"},
		&cli.IntFlag{Name: "vmm-memory-max", Value: 0, Usage: "memory.max (MB) for a sandbox's VMM cgroup; it bounds guest memory plus Firecracker's own footprint, so 0 (unlimited) is the default"},
		&cli.IntFlag{Name: "vcpus", Value: defaultVCPUs, Usage: "Default vCPUs per sandbox"},
		&cli.IntFlag{Name: "memory", Value: defaultMemoryMB, Usage: "Default memory (MB) per sandbox"},
		&cli.DurationFlag{Name: "reap-interval", Value: defaultReapEvery, Usage: "How often idle sandboxes are looked for"},
		&cli.DurationFlag{Name: "default-ttl", Value: 0, Usage: "Idle TTL applied to a sandbox created without one (0 = never reap)"},
		&cli.DurationFlag{Name: "ready-timeout", Value: defaultReadyWindow, Usage: "How long a sandbox's boot may take to reach the guest agent's hello"},
		&cli.StringFlag{Name: "log-level", Value: "info", Usage: "Log level (debug, info, warn, error)"},
	}
}

func run(ctx *cli.Context) error {
	level, err := zerolog.ParseLevel(ctx.String("log-level"))
	if err != nil {
		return fmt.Errorf("--log-level %q: %w", ctx.String("log-level"), err)
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	// The signal context is the daemon's life: the listeners and the reaper both stop
	// with it, and a sandbox does not — a sandbox outlives the command that asked for
	// it, so it outlives a restart of the control plane too.
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The shared v1 surface takes the node's cluster client, like the manager's own
	// API server does. Its failure is reported before anything is opened, because a
	// surface that cannot authenticate a caller must not start.
	cluster, err := client.New(client.Config{StateDir: ctx.String("state-dir")})
	if err != nil {
		return fmt.Errorf("build the node's cluster client (the API surface needs it): %w", err)
	}
	defer func() { _ = cluster.Close() }()

	manager, tokens, err := build(ctx)
	if err != nil {
		return err
	}

	server, err := apihttp.New(apihttp.Config{
		Client:        cluster,
		Tokens:        tokens,
		Sandboxes:     manager,
		CertDir:       certDir(ctx),
		ServerVersion: Version,
		Capabilities:  []string{"read", "write", "sandboxes"},
	})
	if err != nil {
		return fmt.Errorf("build the API server: %w", err)
	}

	// A TCP listener is TLS, and its certificate is the daemon's own when the
	// operator has not provided one: the same bootstrap pair the manager's API
	// serves, self-signed and usable as its own trust root. The Unix listener
	// needs none — the socket's file mode is the credential.
	if tcpAddr := strings.TrimSpace(ctx.String("api-listen")); tcpAddr != "" {
		hostname, _ := os.Hostname()
		if _, _, err := apihttp.EnsureCertificate(certDir(ctx), hostname, certHosts(tcpAddr)); err != nil {
			return fmt.Errorf("prepare the API certificate: %w", err)
		}
	}

	listeners, err := server.Listen(runCtx, ctx.String("socket"), ctx.String("api-listen"))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Info().
		Str("socket", ctx.String("socket")).
		Str("tcp", ctx.String("api-listen")).
		Str("state_dir", ctx.String("state-dir")).
		Str("golden_dir", ctx.String("golden-dir")).
		Dur("reap_interval", ctx.Duration("reap-interval")).
		Msg("sandboxd is serving the sandbox control plane")

	// The listeners and the reaper run until the signal context ends. The reaper runs
	// here, in the one process allowed to write sandbox records, so its decision and
	// the daemon's own state cannot disagree.
	serveErr := make(chan error, 1)
	go func() { serveErr <- listeners.Serve(runCtx) }()
	reapErr := make(chan error, 1)
	go func() { reapErr <- manager.ReapEvery(runCtx, ctx.Duration("reap-interval")) }()

	select {
	case <-runCtx.Done():
		log.Info().Msg("sandboxd is stopping; running sandboxes keep running")
		return nil
	case err := <-serveErr:
		return err
	case err := <-reapErr:
		return err
	}
}

// build maps this command's flags onto the node's sandbox wiring and calls it.
//
// The wiring itself lives in pkg/sandboxd because a host test has to be able to
// build the same manager the daemon builds, from values it names itself; what
// stays here is the part only this command has, which is reading flags.
func build(ctx *cli.Context) (*sandbox.Manager, *apihttp.TokenStore, error) {
	first, err := positiveFlag(ctx, "jailer-uid-first", defaultUIDFirst)
	if err != nil {
		return nil, nil, err
	}
	size, err := positiveFlag(ctx, "jailer-uid-size", defaultUIDSize)
	if err != nil {
		return nil, nil, err
	}

	return sandboxd.New(sandboxd.Config{
		StateDir:        ctx.String("state-dir"),
		GoldenDir:       ctx.String("golden-dir"),
		KernelPath:      ctx.String("kernel-path"),
		KernelProfiles:  kernelProfiles(ctx),
		RootfsDir:       ctx.String("rootfs-dir"),
		LogDir:          ctx.String("log-dir"),
		JailerPath:      ctx.String("jailer-path"),
		JailerChrootDir: ctx.String("jailer-chroot-dir"),
		JailerUIDFirst:  first,
		JailerUIDSize:   size,
		ParentCgroup:    ctx.String("parent-cgroup"),
		VMMPidsMax:      ctx.Int("vmm-pids-max"),
		VMMMemoryMaxMB:  ctx.Int("vmm-memory-max"),
		VCPUs:           ctx.Int("vcpus"),
		MemoryMB:        ctx.Int("memory"),
		DefaultTTL:      ctx.Duration("default-ttl"),
		ReadyTimeout:    ctx.Duration("ready-timeout"),
		TokenFile:       tokenFile(ctx),
	})
}

// positiveFlag parses a numeric flag whose zero means "not set".
func positiveFlag(ctx *cli.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(ctx.String(name))
	if raw == "" {
		return fallback, nil
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil {
		return 0, fmt.Errorf("--%s %q: want a number: %w", name, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("--%s %q: must be positive", name, raw)
	}
	return value, nil
}

// tokenFile is where the API's bearer tokens live: the flag, then the node's own API
// directory, so a token the operator minted with `swarmcracker api token create` is
// the token this daemon accepts.
func tokenFile(ctx *cli.Context) string {
	if path := strings.TrimSpace(ctx.String("api-token-file")); path != "" {
		return path
	}
	return filepath.Join(ctx.String("state-dir"), "api", "tokens.json")
}

// certDir is where the TCP listener's certificate lives.
func certDir(ctx *cli.Context) string {
	if path := strings.TrimSpace(ctx.String("api-cert-dir")); path != "" {
		return path
	}
	return filepath.Join(ctx.String("state-dir"), "api")
}

// certHosts is the host part of a listen address, for the certificate's SANs. A
// wildcard bind has no name a client can verify, so it adds nothing; the loopback
// addresses are in the certificate anyway.
func certHosts(tcpAddr string) []string {
	addr := strings.TrimSpace(tcpAddr)
	if addr == "" {
		return nil
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}
	return []string{host}
}

// kernelProfiles merges the built-in registry with the --kernel-profile overrides, so
// a template's kernel profile resolves to the kernel this node boots it with — the
// same answer the template's pin is checked against.
func kernelProfiles(ctx *cli.Context) map[string]string {
	profiles := config.DefaultKernelProfiles()
	for _, entry := range ctx.StringSlice("kernel-profile") {
		profile, path, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(profile) == "" {
			log.Warn().Str("entry", entry).Msg("Ignoring --kernel-profile: want <profile>=<path>")
			continue
		}
		profiles[strings.TrimSpace(profile)] = strings.TrimSpace(path)
	}
	return profiles
}
