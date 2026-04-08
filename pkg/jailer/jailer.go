// Package jailer provides Firecracker jailer integration for enhanced security isolation.
//
// The jailer creates a chroot environment for each Firecracker VM with:
//   - Cgroup resource limits (CPU, memory, I/O)
//   - Namespace isolation (PID, network, mount)
//   - Seccomp syscall filtering
//   - Privilege dropping (runs as unprivileged user)
//
// Usage:
//
//	cfg := &jailer.Config{
//	    FirecrackerPath: "/usr/local/bin/firecracker",
//	    JailerPath:      "/usr/local/bin/jailer",
//	    ChrootBaseDir:   "/var/lib/swarmcracker/jailer",
//	    UID:             1000,
//	    GID:             1000,
//	    CgroupVersion:   "v2",
//	}
//
//	j := jailer.New(cfg)
//	process, err := j.Start(ctx, jailer.VMConfig{
//	    TaskID:     "vm-123",
//	    VcpuCount:  2,
//	    MemoryMB:   1024,
//	    KernelPath: "/usr/share/firecracker/vmlinux",
//	    RootfsPath: "/var/lib/firecracker/rootfs/rootfs.img",
//	})
package jailer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Jailer manages Firecracker jailer process lifecycle.
type Jailer struct {
	config    *Config
	processes map[string]*Process
	mutex     sync.Mutex
	logger    zerolog.Logger
}

// Config holds jailer configuration.
type Config struct {
	// Path to Firecracker binary
	FirecrackerPath string `yaml:"firecracker_path"`

	// Path to Jailer binary
	JailerPath string `yaml:"jailer_path"`

	// Base directory for jailer chroots
	// Each VM gets a subdirectory: <ChrootBaseDir>/<task-id>/
	ChrootBaseDir string `yaml:"chroot_base_dir"`

	// User ID to run jailed Firecracker processes as
	UID int `yaml:"uid"`

	// Group ID to run jailed Firecracker processes as
	GID int `yaml:"gid"`

	// Network namespace path (optional, for CNI integration)
	NetNS string `yaml:"netns"`

	// Cgroup version: "v1" or "v2"
	CgroupVersion string `yaml:"cgroup_version"`

	// Enable seccomp filtering
	EnableSeccomp bool `yaml:"enable_seccomp"`

	// Seccomp policy file path (optional, uses default if empty)
	SeccompPolicyPath string `yaml:"seccomp_policy_path"`

	// Extra jailer arguments (advanced)
	ExtraArgs []string `yaml:"extra_args"`
}

// VMConfig holds per-VM jailer settings.
type VMConfig struct {
	// Unique task/VM identifier
	TaskID string

	// Number of vCPUs
	VcpuCount int

	// Memory in MB
	MemoryMB int

	// Path to kernel image
	KernelPath string

	// Path to rootfs image
	RootfsPath string

	// Boot arguments (optional)
	BootArgs string

	// Enable hyperthreading/smt
	HtEnabled bool

	// Network interface name (optional)
	NetworkDev string

	// Guest MAC address (optional)
	GuestMac string
}

// Process represents a running jailed Firecracker process.
type Process struct {
	TaskID     string
	Cmd        *exec.Cmd
	SocketPath string
	Pid        int
	StartTime  time.Time
}

// New creates a new Jailer instance.
func New(cfg *Config) (*Jailer, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Validate required fields
	if cfg.FirecrackerPath == "" {
		return nil, fmt.Errorf("FirecrackerPath is required")
	}
	if cfg.JailerPath == "" {
		return nil, fmt.Errorf("JailerPath is required")
	}
	if cfg.ChrootBaseDir == "" {
		return nil, fmt.Errorf("ChrootBaseDir is required")
	}
	if cfg.UID == 0 {
		return nil, fmt.Errorf("UID must be non-zero")
	}
	if cfg.GID == 0 {
		return nil, fmt.Errorf("GID must be non-zero")
	}

	// Set defaults
	if cfg.CgroupVersion == "" {
		cfg.CgroupVersion = "v2"
	}

	// Resolve Firecracker binary path
	firecrackerPath := cfg.FirecrackerPath
	if !filepath.IsAbs(firecrackerPath) {
		resolved, err := exec.LookPath(firecrackerPath)
		if err != nil {
			return nil, fmt.Errorf("Firecracker binary not found: %w", err)
		}
		firecrackerPath = resolved
	}
	if _, err := os.Stat(firecrackerPath); err != nil {
		return nil, fmt.Errorf("Firecracker binary not found at %s: %w", firecrackerPath, err)
	}

	// Resolve Jailer binary path
	jailerPath := cfg.JailerPath
	if !filepath.IsAbs(jailerPath) {
		resolved, err := exec.LookPath(jailerPath)
		if err != nil {
			return nil, fmt.Errorf("Jailer binary not found: %w", err)
		}
		jailerPath = resolved
	}
	if _, err := os.Stat(jailerPath); err != nil {
		return nil, fmt.Errorf("Jailer binary not found at %s: %w", jailerPath, err)
	}

	// Update config with resolved paths
	cfg.FirecrackerPath = firecrackerPath
	cfg.JailerPath = jailerPath

	// Create chroot base directory
	if err := os.MkdirAll(cfg.ChrootBaseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create chroot base dir: %w", err)
	}

	return &Jailer{
		config:    cfg,
		processes: make(map[string]*Process),
		logger:    log.With().Str("component", "jailer").Logger(),
	}, nil
}

// Start launches a Firecracker VM inside jailer.
func (j *Jailer) Start(_ context.Context, cfg VMConfig) (*Process, error) {
	j.logger.Info().
		Str("task_id", cfg.TaskID).
		Int("vcpus", cfg.VcpuCount).
		Int("memory_mb", cfg.MemoryMB).
		Msg("Starting jailed Firecracker VM")

	// Validate VM config
	if err := j.validateVMConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid VM config: %w", err)
	}

	// Build jailer command
	cmd, socketPath, err := j.buildJailerCommand(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build jailer command: %w", err)
	}

	// Start jailer process
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start jailer: %w", err)
	}

	j.logger.Debug().
		Str("task_id", cfg.TaskID).
		Int("pid", cmd.Process.Pid).
		Msg("Jailer process started")

	// Wait for socket to be created
	if err := j.waitForSocket(socketPath, 10*time.Second); err != nil {
		j.logger.Error().Err(err).Msg("Socket not created, killing jailer")
		cmd.Process.Kill()
		return nil, fmt.Errorf("socket not created: %w", err)
	}

	// Create process record
	process := &Process{
		TaskID:     cfg.TaskID,
		Cmd:        cmd,
		SocketPath: socketPath,
		Pid:        cmd.Process.Pid,
		StartTime:  time.Now(),
	}

	// Store process reference
	j.mutex.Lock()
	j.processes[cfg.TaskID] = process
	j.mutex.Unlock()

	j.logger.Info().
		Str("task_id", cfg.TaskID).
		Int("pid", cmd.Process.Pid).
		Str("socket", socketPath).
		Msg("Jailed Firecracker VM started successfully")

	return process, nil
}

// buildJailerCommand constructs the jailer command with all arguments.
func (j *Jailer) buildJailerCommand(cfg VMConfig) (*exec.Cmd, string, error) {
	// Socket path inside chroot
	socketRelPath := filepath.Join("run", "firecracker", cfg.TaskID+".sock")
	socketPath := filepath.Join(j.config.ChrootBaseDir, cfg.TaskID, socketRelPath)

	// Build jailer arguments
	args := []string{
		"--id", cfg.TaskID,
		"--exec-file", j.config.FirecrackerPath,
		"--uid", strconv.Itoa(j.config.UID),
		"--gid", strconv.Itoa(j.config.GID),
		"--chroot-base-dir", j.config.ChrootBaseDir,
		"--cgroup-version", j.config.CgroupVersion,
	}

	// Optional network namespace
	if j.config.NetNS != "" {
		args = append(args, "--netns", j.config.NetNS)
	}

	// Optional seccomp
	if j.config.EnableSeccomp {
		if j.config.SeccompPolicyPath != "" {
			args = append(args, "--seccomp", j.config.SeccompPolicyPath)
		} else {
			// Use default seccomp policy
			defaultPolicyPath, err := j.createDefaultSeccompPolicy(cfg.TaskID)
			if err != nil {
				j.logger.Warn().Err(err).Msg("Failed to create default seccomp policy, continuing without")
			} else {
				args = append(args, "--seccomp", defaultPolicyPath)
			}
		}
	}

	// Add extra arguments
	if len(j.config.ExtraArgs) > 0 {
		args = append(args, j.config.ExtraArgs...)
	}

	// Separator between jailer args and firecracker args
	args = append(args, "--")

	// Firecracker arguments
	firecrackerArgs := []string{
		"--api-sock", filepath.Join("/", socketRelPath),
	}

	args = append(args, firecrackerArgs...)

	j.logger.Debug().
		Str("task_id", cfg.TaskID).
		Strs("args", args).
		Msg("Building jailer command")

	cmd := exec.Command(j.config.JailerPath, args...)
	cmd.Stdout = &logWriter{logger: j.logger.Level(zerolog.DebugLevel)}
	cmd.Stderr = &logWriter{logger: j.logger.Level(zerolog.DebugLevel)}

	return cmd, socketPath, nil
}

// validateVMConfig validates VM configuration.
func (j *Jailer) validateVMConfig(cfg VMConfig) error {
	if cfg.TaskID == "" {
		return fmt.Errorf("TaskID is required")
	}
	if cfg.VcpuCount <= 0 {
		return fmt.Errorf("VcpuCount must be positive")
	}
	if cfg.MemoryMB <= 0 {
		return fmt.Errorf("MemoryMB must be positive")
	}
	if cfg.KernelPath == "" {
		return fmt.Errorf("KernelPath is required")
	}
	if cfg.RootfsPath == "" {
		return fmt.Errorf("RootfsPath is required")
	}

	// Verify kernel exists
	if _, err := os.Stat(cfg.KernelPath); err != nil {
		return fmt.Errorf("kernel not found: %w", err)
	}

	// Verify rootfs exists
	if _, err := os.Stat(cfg.RootfsPath); err != nil {
		return fmt.Errorf("rootfs not found: %w", err)
	}

	return nil
}

// waitForSocket waits for the Firecracker API socket to be created.
func (j *Jailer) waitForSocket(socketPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	j.logger.Debug().
		Str("socket", socketPath).
		Dur("timeout", timeout).
		Msg("Waiting for Firecracker socket")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				j.logger.Debug().Str("socket", socketPath).Msg("Socket created")
				return nil
			}
		}
	}
}

// Stop terminates a jailed VM gracefully.
func (j *Jailer) Stop(_ context.Context, taskID string) error {
	j.logger.Info().
		Str("task_id", taskID).
		Msg("Stopping jailed Firecracker VM")

	j.mutex.Lock()
	process, ok := j.processes[taskID]
	j.mutex.Unlock()

	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}

	// Send SIGTERM for graceful shutdown
	if err := process.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		j.logger.Error().Err(err).Msg("Failed to send SIGTERM")
	}

	// Wait for process to exit or kill after timeout
	done := make(chan error, 1)
	go func() {
		done <- process.Cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			j.logger.Error().Err(err).Msg("Process exited with error")
		}
	case <-time.After(10 * time.Second):
		j.logger.Warn().Msg("Process did not exit gracefully, killing")
		process.Cmd.Process.Kill()
	}

	// Remove from process map
	j.mutex.Lock()
	delete(j.processes, taskID)
	j.mutex.Unlock()

	j.logger.Info().
		Str("task_id", taskID).
		Msg("Jailed Firecracker VM stopped")

	return nil
}

// ForceStop forcefully terminates a jailed VM.
func (j *Jailer) ForceStop(ctx context.Context, taskID string) error {
	j.logger.Warn().
		Str("task_id", taskID).
		Msg("Force stopping jailed Firecracker VM")

	j.mutex.Lock()
	process, ok := j.processes[taskID]
	j.mutex.Unlock()

	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}

	// Force kill immediately
	if err := process.Cmd.Process.Kill(); err != nil {
		j.logger.Error().Err(err).Msg("Failed to kill process")
		return fmt.Errorf("failed to kill process: %w", err)
	}

	// Wait for exit
	_ = process.Cmd.Wait()

	// Remove from process map
	j.mutex.Lock()
	delete(j.processes, taskID)
	j.mutex.Unlock()

	j.logger.Info().
		Str("task_id", taskID).
		Msg("Jailed Firecracker VM force stopped")

	return nil
}

// GetProcess returns the process for a task.
func (j *Jailer) GetProcess(taskID string) (*Process, bool) {
	j.mutex.Lock()
	defer j.mutex.Unlock()

	process, ok := j.processes[taskID]
	return process, ok
}

// ListProcesses returns all running jailed VMs.
func (j *Jailer) ListProcesses() []string {
	j.mutex.Lock()
	defer j.mutex.Unlock()

	taskIDs := make([]string, 0, len(j.processes))
	for taskID := range j.processes {
		taskIDs = append(taskIDs, taskID)
	}
	return taskIDs
}

// Close cleans up all jailer resources.
func (j *Jailer) Close() error {
	j.mutex.Lock()
	defer j.mutex.Unlock()

	var errs []string
	for taskID, process := range j.processes {
		j.logger.Info().
			Str("task_id", taskID).
			Msg("Stopping VM during cleanup")

		if err := process.Cmd.Process.Kill(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", taskID, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, ", "))
	}

	return nil
}

// createDefaultSeccompPolicy creates a default seccomp policy file.
func (j *Jailer) createDefaultSeccompPolicy(taskID string) (string, error) {
	// Default policy for Firecracker
	policy := `{
		"defaultAction": "kill",
		"syscalls": [
			{
				"action": "allow",
				"names": [
					"accept", "accept4", "access", "arch_prctl", "bind", "brk",
					"capget", "capset", "chdir", "chmod", "chown", "chown32",
					"clock_getres", "clock_gettime", "clock_nanosleep", "clone",
					"close", "connect", "dup", "dup2", "dup3", "epoll_create",
					"epoll_create1", "epoll_ctl", "epoll_pwait", "epoll_wait",
					"eventfd", "eventfd2", "execve", "exit", "exit_group",
					"fcntl", "fcntl64", "fdatasync", "fgetxattr", "flistxattr",
					"flock", "fork", "fsetxattr", "fstat", "fstat64", "fstatat64",
					"fstatfs", "fstatfs64", "fsync", "ftruncate", "ftruncate64",
					"futex", "getcwd", "getdents", "getdents64", "getegid",
					"getegid32", "geteuid", "geteuid32", "getgid", "getgid32",
					"getgroups", "getgroups32", "getpeername", "getpgrp", "getpid",
					"getppid", "getpriority", "getrandom", "getresgid", "getresgid32",
					"getresuid", "getresuid32", "getrlimit", "getrusage", "getsid",
					"getsockname", "getsockopt", "gettid", "gettimeofday", "getuid",
					"getuid32", "getxattr", "inotify_add_watch", "inotify_init",
					"inotify_init1", "inotify_rm_watch", "ioctl", "ioprio_get",
					"ioprio_set", "kill", "lgetxattr", "link", "linkat", "listen",
					"llistxattr", "lseek", "lsetxattr", "lstat", "lstat64", "madvise",
					"memfd_create", "mincore", "mkdir", "mkdirat", "mknod", "mknodat",
					"mlock", "mlock2", "mlockall", "mmap", "mmap2", "mprotect",
					"mremap", "msync", "munlock", "munlockall", "munmap", "nanosleep",
					"newfstatat", "_newselect", "open", "openat", "pause", "pipe",
					"pipe2", "poll", "ppoll", "prctl", "pread64", "preadv", "prlimit64",
					"pselect6", "pwrite64", "pwritev", "read", "readahead", "readlink",
					"readlinkat", "readv", "recv", "recvfrom", "recvmmsg", "recvmsg",
					"rename", "renameat", "renameat2", "restart_syscall", "rmdir",
					"rt_sigaction", "rt_sigpending", "rt_sigprocmask", "rt_sigqueueinfo",
					"rt_sigreturn", "rt_sigsuspend", "rt_sigtimedwait", "rt_tgsigqueueinfo",
					"sched_getaffinity", "sched_getattr", "sched_getparam", "sched_get_priority_max",
					"sched_get_priority_min", "sched_getscheduler", "sched_rr_get_interval",
					"sched_setaffinity", "sched_setattr", "sched_setparam", "sched_setscheduler",
					"sched_yield", "seccomp", "select", "semctl", "semget", "semop", "semtimedop",
					"send", "sendfile", "sendfile64", "sendmmsg", "sendmsg", "sendto",
					"setfsgid", "setfsgid32", "setfsuid", "setfsuid32", "setgid", "setgid32",
					"setgroups", "setgroups32", "setitimer", "setpgid", "setpriority",
					"setregid", "setregid32", "setresgid", "setresgid32", "setresuid",
					"setresuid32", "setreuid", "setreuid32", "setrlimit", "setsid",
					"setsockopt", "setuid", "setuid32", "setxattr", "shmat", "shmctl",
					"shmdt", "shmget", "shutdown", "sigaltstack", "signalfd", "signalfd4",
					"socket", "socketcall", "socketpair", "splice", "stat", "stat64",
					"statfs", "statfs64", "statx", "symlink", "symlinkat", "sync",
					"sync_file_range", "syncfs", "sysinfo", "tee", "tgkill", "time",
					"timer_create", "timer_delete", "timerfd_create", "timerfd_gettime",
					"timerfd_settime", "timer_getoverrun", "timer_gettime", "timer_settime",
					"times", "tkill", "truncate", "truncate64", "ugetrlimit", "umask",
					"uname", "unlink", "unlinkat", "utime", "utimensat", "utimes",
					"vfork", "vmsplice", "wait4", "waitid", "waitpid", "write", "writev"
				]
			}
		]
	}`

	// Ensure chroot base directory exists
	if err := os.MkdirAll(j.config.ChrootBaseDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create chroot base dir: %w", err)
	}

	policyPath := filepath.Join(j.config.ChrootBaseDir, taskID+".seccomp.json")
	if err := os.WriteFile(policyPath, []byte(policy), 0644); err != nil {
		return "", fmt.Errorf("failed to write seccomp policy: %w", err)
	}

	return policyPath, nil
}

// logWriter writes log lines to zerolog.
type logWriter struct {
	logger zerolog.Logger
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.logger.Debug().Msg(strings.TrimSpace(string(p)))
	return len(p), nil
}
