package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// SeccompFilter defines a seccomp filter for Firecracker VMs
type SeccompFilter struct {
	DefaultAction string        `json:"defaultAction"`
	Architectures []string      `json:"architectures"`
	Syscalls      []SyscallRule `json:"syscalls"`
}

// SyscallRule defines a rule for system calls
type SyscallRule struct {
	Names  []string `json:"names"`
	Action string   `json:"action"`
	Args   []Arg    `json:"args,omitempty"`
}

// Arg defines argument constraints for syscalls
type Arg struct {
	Index    uint   `json:"index"`
	Value    uint64 `json:"value"`
	ValueTwo uint64 `json:"valueTwo,omitempty"`
	Op       string `json:"op"`
}

// DefaultSeccompFilter returns a restrictive seccomp profile for container workloads
func DefaultSeccompFilter() *SeccompFilter {
	return &SeccompFilter{
		DefaultAction: "SCMP_ACT_ERRNO",
		Architectures: []string{"SCMP_ARCH_X86_64", "SCMP_ARCH_X86"},
		Syscalls: []SyscallRule{
			// Basic I/O operations
			{Names: []string{"read", "write", "open", "close", "stat", "fstat", "lstat", "poll"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"lseek", "mmap", "mprotect", "munmap", "brk"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"rt_sigaction", "rt_sigprocmask", "rt_sigreturn"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"ioctl", "pread64", "pwrite64", "readv", "writev"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"access", "pipe", "select", "sched_yield", "mremap"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"msync", "mincore", "madvise"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"dup", "dup2", "pause", "nanosleep", "getitimer", "alarm"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"setitimer", "getpid", "sendfile", "socket", "connect"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"accept", "sendto", "recvfrom", "sendmsg", "recvmsg"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"shutdown", "bind", "listen", "getsockname", "getpeername"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"socketpair", "setsockopt", "getsockopt", "clone"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"fork", "vfork", "execve", "exit", "wait4", "kill", "uname"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"fcntl", "flock", "fsync", "fdatasync", "truncate", "ftruncate"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"getdents", "getcwd", "chdir", "fchdir", "rename", "mkdir", "rmdir"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"creat", "link", "unlink", "symlink", "readlink", "chmod", "fchmod"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"chown", "fchown", "lchown", "umask", "gettimeofday", "getrlimit"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"getrusage", "sysinfo", "times", "getuid", "getgid", "setuid", "setgid"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"geteuid", "getegid", "setpgid", "getppid", "getpgrp", "setsid"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"setreuid", "setregid", "getgroups", "setgroups", "setresuid", "setresgid"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"getresuid", "getresgid", "getpgid", "setfsuid", "setfsgid", "getsid"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"capget", "capset", "rt_sigpending", "rt_sigtimedwait", "rt_sigqueueinfo"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"sigaltstack", "utime", "mknod", "uselib", "personality"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"ustat", "statfs", "fstatfs", "sysfs", "getpriority", "setpriority"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"sched_setparam", "sched_getparam", "sched_setscheduler", "sched_getscheduler"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"sched_get_priority_max", "sched_get_priority_min", "sched_rr_get_interval"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"mlock", "munlock", "mlockall", "munlockall", "mprotect", "sigprocmask"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"getuid32", "getgid32", "setuid32", "setgid32", "geteuid32", "getegid32"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"setreuid32", "setregid32", "getgroups32", "setgroups32", "fchown32"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"setresuid32", "getresuid32", "setresgid32", "getresgid32", "chown32"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"setuid32", "setgid32", "setfsuid32", "setfsgid32"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"pivot_root", "prctl", "arch_prctl", "adjtimex", "setrlimit"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"chroot", "sync", "acct", "settimeofday", "mount", "umount2"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"swapon", "swapoff", "reboot", "sethostname", "setdomainname"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"iopl", "ioperm", "init_module", "delete_module"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"quotactl", "gettid", "readahead", "setxattr", "lsetxattr", "fsetxattr"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"getxattr", "lgetxattr", "fgetxattr", "listxattr", "llistxattr", "flistxattr"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"removexattr", "lremovexattr", "fremovexattr", "tkill", "time", "futex"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"sched_setaffinity", "sched_getaffinity", "set_thread_area", "io_setup"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"io_destroy", "io_getevents", "io_submit", "io_cancel", "get_thread_area"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"epoll_create", "epoll_ctl", "epoll_wait", "remap_file_pages", "getdents64"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"set_tid_address", "restart_syscall", "semtimedop", "fadvise64", "timer_create"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"timer_settime", "timer_gettime", "timer_getoverrun", "timer_delete", "clock_settime"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"clock_gettime", "clock_getres", "clock_nanosleep", "exit_group", "epoll_wait"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"epoll_ctl", "tgkill", "utimes", "mbind", "set_mempolicy"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"get_mempolicy", "mq_open", "mq_unlink", "mq_timedsend", "mq_timedreceive"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"mq_notify", "mq_getsetattr", "kexec_load", "waitid", "add_key"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"request_key", "keyctl", "ioprio_set", "ioprio_get", "inotify_init"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"inotify_add_watch", "inotify_rm_watch", "migrate_pages", "openat", "mkdirat"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"mknodat", "fchownat", "futimesat", "newfstatat", "unlinkat"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"renameat", "linkat", "symlinkat", "readlinkat", "fchmodat", "faccessat"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"pselect6", "ppoll", "unshare", "set_robust_list", "get_robust_list"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"splice", "tee", "sync_file_range", "vmsplice", "move_pages"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"utimensat", "epoll_pwait", "signalfd", "timerfd_create", "eventfd"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"fallocate", "timerfd_settime", "timerfd_gettime", "accept4", "signalfd4"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"epoll_create2", "epoll_ctl", "epoll_wait", "dup3", "pipe2"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"inotify_init1", "preadv", "pwritev", "rt_tgsigqueueinfo", "perf_event_open"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"recvmmsg", "sendmmsg", "setns", "getcpu", "process_vm_readv"}, Action: "SCMP_ACT_ALLOW"},
			{Names: []string{"process_vm_writev"}, Action: "SCMP_ACT_ALLOW"},
		},
	}
}

// WriteSeccompProfile writes a seccomp profile to a file for Firecracker
func WriteSeccompProfile(vmID, profilePath string) error {
	filter := DefaultSeccompFilter()

	data, err := json.MarshalIndent(filter, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal seccomp filter: %w", err)
	}

	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(profilePath), 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}

	if err := os.WriteFile(profilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write seccomp profile: %w", err)
	}

	log.Info().
		Str("vm_id", vmID).
		Str("profile_path", profilePath).
		Msg("Seccomp profile written")

	return nil
}

// ValidateSeccompProfile checks if a seccomp profile is valid
func ValidateSeccompProfile(profilePath string) error {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return fmt.Errorf("failed to read seccomp profile: %w", err)
	}

	var filter SeccompFilter
	if err := json.Unmarshal(data, &filter); err != nil {
		return fmt.Errorf("failed to parse seccomp profile: %w", err)
	}

	// Validate default action
	if filter.DefaultAction != "SCMP_ACT_ALLOW" &&
	   filter.DefaultAction != "SCMP_ACT_DENY" &&
	   filter.DefaultAction != "SCMP_ACT_ERRNO" &&
	   filter.DefaultAction != "SCMP_ACT_KILL" {
		return fmt.Errorf("invalid default action: %s", filter.DefaultAction)
	}

	// Validate architectures
	validArchs := map[string]bool{
		"SCMP_ARCH_X86_64": true,
		"SCMP_ARCH_X86":    true,
		"SCMP_ARCH_ARM":    true,
		"SCMP_ARCH_AARCH64": true,
	}
	for _, arch := range filter.Architectures {
		if !validArchs[arch] {
			return fmt.Errorf("invalid architecture: %s", arch)
		}
	}

	log.Debug().Str("profile_path", profilePath).Msg("Seccomp profile validated")
	return nil
}

// RestrictiveSeccompFilter returns a more restrictive filter for high-security scenarios
func RestrictiveSeccompFilter() *SeccompFilter {
	filter := DefaultSeccompFilter()

	// Block dangerous syscalls even if in default list
	blockedSyscalls := []string{
		"mount", "umount2", "pivot_root", "chroot",
		"init_module", "delete_module", "kexec_load",
		"swapon", "swapoff", "reboot",
		"setuid", "setgid", "setreuid", "setregid",
		"setresuid", "setresgid", "setfsuid", "setfsgid",
	}

	// Remove blocked syscalls from allowed list
	var filteredRules []SyscallRule
	for _, rule := range filter.Syscalls {
		if rule.Action != "SCMP_ACT_ALLOW" {
			filteredRules = append(filteredRules, rule)
			continue
		}

		var allowedNames []string
		for _, name := range rule.Names {
			blocked := false
			for _, blockedName := range blockedSyscalls {
				if name == blockedName {
					blocked = true
					break
				}
			}
			if !blocked {
				allowedNames = append(allowedNames, name)
			}
		}

		if len(allowedNames) > 0 {
			rule.Names = allowedNames
			filteredRules = append(filteredRules, rule)
		}
	}

	filter.Syscalls = filteredRules
	return filter
}
