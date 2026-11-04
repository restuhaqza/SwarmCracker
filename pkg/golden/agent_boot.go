package golden

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// DefaultFirstBootTimeout bounds how long a freshly built template may take to
// reach its guest agent. It is generous because the first boot of a full OS
// image writes a fresh machine-id and starts its init system from scratch.
const DefaultFirstBootTimeout = 3 * time.Minute

// BootTarget describes one artifact to boot for verification.
type BootTarget struct {
	// ArtifactPath is the ext4 image to boot.
	ArtifactPath string
	// KernelPath is the vmlinux the recipe's kernel profile resolves to.
	KernelPath string
	// InitSystem is the recipe's init system; it decides init=.
	InitSystem string
	// BootArgs are the recipe's extra kernel arguments.
	BootArgs []string
}

// BootProber boots a freshly built artifact and returns the hello its guest
// agent answered with. The builder requires this: a template whose agent never
// answers cannot serve exec, files, health or prepare, so producing one would
// only move the failure to every sandbox made from it.
type BootProber interface {
	Probe(ctx context.Context, target BootTarget) (agent.Hello, error)
}

// FirecrackerProber boots an artifact under Firecracker and negotiates the v2
// handshake over the same vsock path the executor uses. It needs KVM and the
// firecracker binary, which is why a template build is a KVM-host operation.
type FirecrackerProber struct {
	// Binary is the firecracker executable; empty means "firecracker" on PATH.
	Binary string
	// BootTimeout bounds the wait for the guest agent; zero means
	// DefaultFirstBootTimeout.
	BootTimeout time.Duration
	// VCPUs and MemoryMiB size the probe VM. Zero means 2 vCPUs and 2048 MiB,
	// enough for a full OS guest to reach multi-user.
	VCPUs     int64
	MemoryMiB int64

	// launch starts the VM and dial connects to its agent. They are seams, not
	// configuration: nil means the real Firecracker process and the real vsock
	// dial, so the failure paths (start failure, early exit, timeout, dial
	// refused) can be exercised without KVM.
	launch vmLauncher
	dial   helloDialer
}

// vmProcess is a VM the prober started. done reports the exit error exactly
// once, and kill terminates it.
type vmProcess interface {
	done() <-chan error
	kill()
}

// vmLauncher starts a VM from a full Firecracker argv and sends its console
// output to output.
type vmLauncher func(ctx context.Context, argv []string, output io.Writer) (vmProcess, error)

// helloClient is the part of agent.Client the prober uses.
type helloClient interface {
	Hello() agent.Hello
	Close() error
}

// helloDialer connects to a guest agent over a Firecracker vsock muxer.
type helloDialer func(ctx context.Context, udsPath string, port uint32) (helloClient, error)

// NewFirecrackerProber returns a prober with the default budget and VM size.
func NewFirecrackerProber() *FirecrackerProber { return &FirecrackerProber{} }

func (p *FirecrackerProber) binary() string {
	if p.Binary != "" {
		return p.Binary
	}
	return "firecracker"
}

func (p *FirecrackerProber) launcher() vmLauncher {
	if p.launch != nil {
		return p.launch
	}
	return startFirecracker
}

func (p *FirecrackerProber) dialer() helloDialer {
	if p.dial != nil {
		return p.dial
	}
	return dialAgent
}

func (p *FirecrackerProber) timeout() time.Duration {
	if p.BootTimeout > 0 {
		return p.BootTimeout
	}
	return DefaultFirstBootTimeout
}

func (p *FirecrackerProber) vcpus() int64 {
	if p.VCPUs > 0 {
		return p.VCPUs
	}
	return 2
}

func (p *FirecrackerProber) memMiB() int64 {
	if p.MemoryMiB > 0 {
		return p.MemoryMiB
	}
	return 2048
}

// firecrackerConfig is the subset of Firecracker's config file this prober
// needs. The vsock device is the point: without it the host has no path to the
// guest agent, so a probe that omitted it would prove nothing.
type firecrackerConfig struct {
	BootSource struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArgs        string `json:"boot_args"`
	} `json:"boot-source"`
	Drives []struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	} `json:"drives"`
	MachineConfig struct {
		VCPUCount  int64 `json:"vcpu_count"`
		MemSizeMiB int64 `json:"mem_size_mib"`
	} `json:"machine-config"`
	Vsock struct {
		GuestCID uint32 `json:"guest_cid"`
		UDSPath  string `json:"uds_path"`
	} `json:"vsock"`
}

// Probe implements BootProber.
func (p *FirecrackerProber) Probe(ctx context.Context, target BootTarget) (agent.Hello, error) {
	if target.ArtifactPath == "" {
		return agent.Hello{}, fmt.Errorf("golden: probe needs an artifact path")
	}
	if target.KernelPath == "" {
		return agent.Hello{}, fmt.Errorf("golden: probe needs a kernel image")
	}
	if _, err := os.Stat(target.KernelPath); err != nil {
		return agent.Hello{}, fmt.Errorf("golden: kernel image %s: %w", target.KernelPath, err)
	}

	dir, err := os.MkdirTemp("", "swarmcracker-probe-")
	if err != nil {
		return agent.Hello{}, fmt.Errorf("golden: create probe dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// Boot a copy: a first boot writes a machine-id, journals and service
	// state, and the sealed artifact must stay byte-identical to its checksum.
	rootfs := filepath.Join(dir, "rootfs.ext4")
	if err := copyFileSparse(target.ArtifactPath, rootfs); err != nil {
		return agent.Hello{}, err
	}

	uds := filepath.Join(dir, "vsock.sock")
	apiSock := filepath.Join(dir, "api.sock")
	configPath := filepath.Join(dir, "firecracker.json")
	logPath := filepath.Join(dir, "serial.log")

	cfg := firecrackerConfig{}
	cfg.BootSource.KernelImagePath = target.KernelPath
	bootArgs, err := probeBootArgs(target.InitSystem, target.BootArgs)
	if err != nil {
		return agent.Hello{}, err
	}
	cfg.BootSource.BootArgs = bootArgs
	cfg.Drives = append(cfg.Drives, struct {
		DriveID      string `json:"drive_id"`
		PathOnHost   string `json:"path_on_host"`
		IsRootDevice bool   `json:"is_root_device"`
		IsReadOnly   bool   `json:"is_read_only"`
	}{DriveID: "rootfs", PathOnHost: rootfs, IsRootDevice: true, IsReadOnly: false})
	cfg.MachineConfig.VCPUCount = p.vcpus()
	cfg.MachineConfig.MemSizeMiB = p.memMiB()
	cfg.Vsock.GuestCID = agent.GuestCID
	cfg.Vsock.UDSPath = uds

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return agent.Hello{}, fmt.Errorf("golden: marshal probe config: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return agent.Hello{}, fmt.Errorf("golden: write probe config: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return agent.Hello{}, fmt.Errorf("golden: create probe log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	proc, err := p.launcher()(ctx, probeArgv(p.binary(), apiSock, configPath), logFile)
	if err != nil {
		return agent.Hello{}, fmt.Errorf("golden: start %s: %w", p.binary(), err)
	}
	defer func() {
		proc.kill()
		select {
		case <-proc.done():
		case <-time.After(5 * time.Second):
		}
	}()

	deadline := time.Now().Add(p.timeout())
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		client, dialErr := p.dialer()(attemptCtx, uds, agent.DefaultPort)
		cancel()
		if dialErr == nil {
			hello := client.Hello()
			_ = client.Close()
			return hello, nil
		}

		select {
		case waitErr := <-proc.done():
			return agent.Hello{}, fmt.Errorf("firecracker exited before the guest agent answered: %v\n%s",
				waitErr, probeLogTail(logPath))
		default:
		}

		if ctx.Err() != nil {
			return agent.Hello{}, fmt.Errorf("wait for guest agent: %w\n%s", ctx.Err(), probeLogTail(logPath))
		}
		if time.Now().After(deadline) {
			return agent.Hello{}, fmt.Errorf("guest agent did not answer hello within %s: %v\n%s",
				p.timeout(), dialErr, probeLogTail(logPath))
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// probeArgv is the Firecracker command line the probe runs.
func probeArgv(binary, apiSock, configPath string) []string {
	return []string{binary, "--api-sock", apiSock, "--config-file", configPath}
}

// startFirecracker is the real vmLauncher.
func startFirecracker(ctx context.Context, argv []string, output io.Writer) (vmProcess, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // binary is operator-configured
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc := &firecrackerProcess{cmd: cmd, waited: make(chan error, 1)}
	go func() { proc.waited <- cmd.Wait() }()
	return proc, nil
}

// firecrackerProcess is the real vmProcess: an exec.Cmd whose exit is delivered
// on a buffered channel, so the prober can notice an early exit without
// blocking and the cleanup can still drain it.
type firecrackerProcess struct {
	cmd    *exec.Cmd
	waited chan error
}

func (p *firecrackerProcess) done() <-chan error { return p.waited }

func (p *firecrackerProcess) kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// dialAgent connects to the guest agent behind a Firecracker vsock muxer. It
// presents no token: a template is probed before the node ever provisions one,
// so a hello here is exactly what an unprepared guest answers.
func dialAgent(ctx context.Context, udsPath string, port uint32) (helloClient, error) {
	client, err := agent.DialWithToken(ctx, udsPath, port, "")
	if err != nil {
		return nil, err
	}
	return client, nil
}

// verifyFirstBoot boots the finished artifact and holds its guest agent to the
// handshake the host will perform on every task. It returns the pin the
// artifact actually carries: the protocol and agent version the guest reported,
// which is what a template may claim. A recipe that pins a version explicitly
// must match, so a pin can never describe an agent the image does not contain.
func (b *Builder) verifyFirstBoot(ctx context.Context, r *Recipe, artifactPath string) (AgentPin, error) {
	ref := r.Metadata.Name + "@" + r.Metadata.Version
	kernel := b.opts.KernelProfiles[r.Spec.Kernel.Profile]
	if kernel == "" {
		return AgentPin{}, fmt.Errorf(
			"golden: kernel profile %q of %s is not mapped to a kernel image, so the first boot cannot be verified",
			r.Spec.Kernel.Profile, ref)
	}

	hello, err := b.opts.Prober.Probe(ctx, BootTarget{
		ArtifactPath: artifactPath,
		KernelPath:   kernel,
		InitSystem:   r.Spec.Init.System,
		BootArgs:     r.Spec.Init.BootArgs,
	})
	if err != nil {
		return AgentPin{}, fmt.Errorf("golden: first boot of %s did not reach its guest agent: %w", ref, err)
	}

	if hello.Version != r.Spec.Agent.Protocol {
		return AgentPin{}, fmt.Errorf(
			"golden: %s pins guest agent protocol %q but the agent in the image answered %q",
			ref, r.Spec.Agent.Protocol, hello.Version)
	}

	var missing []string
	for _, cap := range agent.Capabilities() {
		if !hello.Supports(cap) {
			missing = append(missing, cap)
		}
	}
	if len(missing) > 0 {
		return AgentPin{}, fmt.Errorf(
			"golden: %s guest agent answered protocol %s without advertising %s; the host refuses operations a guest does not advertise",
			ref, hello.Version, strings.Join(missing, ", "))
	}

	if hello.AgentVersion == "" {
		return AgentPin{}, fmt.Errorf("golden: %s guest agent answered without reporting its version", ref)
	}
	if r.Spec.Agent.Version != "" && r.Spec.Agent.Version != hello.AgentVersion {
		return AgentPin{}, fmt.Errorf(
			"golden: %s pins guest agent version %q but the agent in the image answered %q",
			ref, r.Spec.Agent.Version, hello.AgentVersion)
	}

	return AgentPin{Protocol: hello.Version, Version: hello.AgentVersion}, nil
}

// probeBootArgs builds the kernel command line the executor uses for a golden
// guest, plus the recipe's own arguments. Every golden init system bootstraps
// through /sbin/init; a recipe naming anything else has no init path this prober
// can prove, so it is refused rather than booted with a guessed one.
func probeBootArgs(initSystem string, extra []string) (string, error) {
	switch initSystem {
	case InitSystemd, InitOpenRC, InitSysvinit:
	default:
		return "", fmt.Errorf("golden: init system %q has no /sbin/init to boot", initSystem)
	}
	args := make([]string, 0, 6+len(extra))
	args = append(args,
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"nomodules",
		"init=/sbin/init",
	)
	args = append(args, extra...)
	return strings.Join(args, " "), nil
}

// probeLogTail returns the end of the guest console log, which carries the init
// system's own explanation of why a boot failed.
func probeLogTail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return tail(string(data), 8192)
}
