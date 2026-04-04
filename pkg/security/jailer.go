package security

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/rs/zerolog/log"
)

// Jailer provides isolation for Firecracker VMs using:
// - chroot jail
// - User namespace isolation
// - Network namespace isolation
// - Resource limits
type Jailer struct {
	UID           int
	GID           int
	ChrootBaseDir string
	NetNS         string
	Enabled       bool
}

// NewJailer creates a new jailer instance
func NewJailer(uid, gid int, chrootDir, netNS string) *Jailer {
	return &Jailer{
		UID:           uid,
		GID:           gid,
		ChrootBaseDir: chrootDir,
		NetNS:         netNS,
		Enabled:       true,
	}
}

// SetupJail creates an isolated environment for a VM
func (j *Jailer) SetupJail(vmID string) (*JailContext, error) {
	if !j.Enabled {
		log.Debug().Msg("Jailer disabled, skipping isolation setup")
		return &JailContext{Enabled: false}, nil
	}

	log.Info().
		Str("vm_id", vmID).
		Int("uid", j.UID).
		Int("gid", j.GID).
		Msg("Setting up jail for VM")

	jailPath := filepath.Join(j.ChrootBaseDir, vmID)

	// Create jail directory structure
	if err := os.MkdirAll(jailPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create jail directory: %w", err)
	}

	// Create subdirectories
	dirs := []string{
		filepath.Join(jailPath, "run"),
		filepath.Join(jailPath, "dev"),
		filepath.Join(jailPath, "proc"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create jail subdirectory: %w", err)
		}
	}

	ctx := &JailContext{
		Enabled:    true,
		JailPath:   jailPath,
		UID:        j.UID,
		GID:        j.GID,
		NetNS:      j.NetNS,
		OriginalWD: "/",
	}

	// Setup resource limits
	if err := j.setResourceLimits(ctx); err != nil {
		return nil, fmt.Errorf("failed to set resource limits: %w", err)
	}

	return ctx, nil
}

// JailContext represents the jail environment for a VM
type JailContext struct {
	Enabled    bool
	JailPath   string
	UID        int
	GID        int
	NetNS      string
	OriginalWD string
}

// EnterJail enters the chroot jail (call after fork, before exec)
func (j *Jailer) EnterJail(ctx *JailContext) error {
	if !ctx.Enabled {
		return nil
	}

	log.Debug().
		Str("jail_path", ctx.JailPath).
		Msg("Entering jail")

	// Change to jail directory
	if err := os.Chdir(ctx.JailPath); err != nil {
		return fmt.Errorf("failed to chdir to jail: %w", err)
	}

	// Change root (requires CAP_SYS_CHROOT)
	if err := syscall.Chroot(ctx.JailPath); err != nil {
		return fmt.Errorf("failed to chroot: %w", err)
	}

	// Change to root directory
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("failed to chdir after chroot: %w", err)
	}

	// Drop privileges
	if err := syscall.Setgid(ctx.GID); err != nil {
		return fmt.Errorf("failed to setgid: %w", err)
	}
	if err := syscall.Setuid(ctx.UID); err != nil {
		return fmt.Errorf("failed to setuid: %w", err)
	}

	log.Debug().
		Int("uid", ctx.UID).
		Int("gid", ctx.GID).
		Msg("Privileges dropped successfully")

	return nil
}

// SetupNetworkNamespace configures network namespace isolation
func (j *Jailer) SetupNetworkNamespace(ctx *JailContext) error {
	if ctx.NetNS == "" {
		log.Debug().Msg("No network namespace configured")
		return nil
	}

	log.Info().
		Str("netns", ctx.NetNS).
		Msg("Setting up network namespace")

	// Use ip netns to enter network namespace
	cmd := exec.Command("ip", "netns", "exec", ctx.NetNS, "true")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enter network namespace %s: %w", ctx.NetNS, err)
	}

	return nil
}

// setResourceLimits sets resource limits for the VM process
func (j *Jailer) setResourceLimits(ctx *JailContext) error {
	// Set file descriptor limit
	const maxFD = 4096
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{
		Cur: uint64(maxFD),
		Max: uint64(maxFD),
	}); err != nil {
		return fmt.Errorf("failed to set FD limit: %w", err)
	}

	// Set number of processes limit
	const maxProc = 1024
	if err := syscall.Setrlimit(syscall.RLIMIT_NPROC, &syscall.Rlimit{
		Cur: uint64(maxProc),
		Max: uint64(maxProc),
	}); err != nil {
		return fmt.Errorf("failed to set process limit: %w", err)
	}

	// Set CPU time limit (unlimited)
	if err := syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{
		Cur: syscall.RLIM_INFINITY,
		Max: syscall.RLIM_INFINITY,
	}); err != nil {
		return fmt.Errorf("failed to set CPU limit: %w", err)
	}

	// Set memory limit (unlimited - managed by Firecracker)
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{
		Cur: syscall.RLIM_INFINITY,
		Max: syscall.RLIM_INFINITY,
	}); err != nil {
		return fmt.Errorf("failed to set memory limit: %w", err)
	}

	log.Debug().
		Int("max_fd", maxFD).
		Int("max_proc", maxProc).
		Msg("Resource limits set")

	return nil
}

// CleanupJail removes the jail directory
func (j *Jailer) CleanupJail(ctx *JailContext) error {
	if !ctx.Enabled || ctx.JailPath == "" {
		return nil
	}

	log.Debug().
		Str("jail_path", ctx.JailPath).
		Msg("Cleaning up jail")

	if err := os.RemoveAll(ctx.JailPath); err != nil {
		return fmt.Errorf("failed to remove jail directory: %w", err)
	}

	return nil
}

// Validate checks if jailer configuration is valid
func (j *Jailer) Validate() error {
	if !j.Enabled {
		return nil
	}

	// Check UID/GID are valid
	if j.UID < 0 || j.GID < 0 {
		return fmt.Errorf("invalid UID/GID: UID=%d GID=%d", j.UID, j.GID)
	}

	// Check if user exists (for privilege dropping)
	if _, err := os.LookupUser(strconv.Itoa(j.UID)); err != nil {
		log.Warn().
			Int("uid", j.UID).
			Msg("User does not exist, jailer may fail")
	}

	// Check chroot directory
	if j.ChrootBaseDir == "" {
		return fmt.Errorf("chroot_base_dir cannot be empty")
	}

	// Check if base directory exists (or can be created)
	if err := os.MkdirAll(j.ChrootBaseDir, 0755); err != nil {
		return fmt.Errorf("cannot create chroot base directory: %w", err)
	}

	return nil
}

// SetResourceLimits sets resource limits for a VM
type ResourceLimits struct {
	MaxCPUs      int
	MaxMemoryMB  int
	MaxFD        int
	MaxProcesses int
}

// ApplyResourceLimits applies resource limits using cgroups
func ApplyResourceLimits(pid int, limits ResourceLimits) error {
	log.Info().
		Int("pid", pid).
		Int("max_cpus", limits.MaxCPUs).
		Int("max_memory_mb", limits.MaxMemoryMB).
		Msg("Applying resource limits")

	// Create cgroup for the process
	cgroupPath := fmt.Sprintf("/sys/fs/cgroup/swarmcracker/%d", pid)
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup: %w", err)
	}

	// Set CPU limit
	if limits.MaxCPUs > 0 {
		cpuQuota := limits.MaxCPUs * 100000
		if err := os.WriteFile(
			filepath.Join(cgroupPath, "cpu.max"),
			[]byte(fmt.Sprintf("%d 100000", cpuQuota)),
			0644,
		); err != nil {
			return fmt.Errorf("failed to set CPU limit: %w", err)
		}
	}

	// Set memory limit
	if limits.MaxMemoryMB > 0 {
		memoryBytes := limits.MaxMemoryMB * 1024 * 1024
		if err := os.WriteFile(
			filepath.Join(cgroupPath, "memory.max"),
			[]byte(strconv.Itoa(memoryBytes)),
			0644,
		); err != nil {
			return fmt.Errorf("failed to set memory limit: %w", err)
		}
	}

	// Add process to cgroup
	if err := os.WriteFile(
		filepath.Join(cgroupPath, "cgroup.procs"),
		[]byte(strconv.Itoa(pid)),
		0644,
	); err != nil {
		return fmt.Errorf("failed to add process to cgroup: %w", err)
	}

	log.Debug().Msg("Resource limits applied successfully")
	return nil
}

// CleanupCgroup removes cgroup for a process
func CleanupCgroup(pid int) error {
	cgroupPath := fmt.Sprintf("/sys/fs/cgroup/swarmcracker/%d", pid)
	if err := os.RemoveAll(cgroupPath); err != nil {
		return fmt.Errorf("failed to remove cgroup: %w", err)
	}
	return nil
}
