// Package jailer provides cgroup v2 management for resource limits.
package jailer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Locations of the host CPU and NUMA-node lists. They are fields on the
// manager so tests can point them at a fixture instead of /sys.
const (
	defaultCPUOnlinePath  = "/sys/devices/system/cpu/online"
	defaultNodeOnlinePath = "/sys/devices/system/node/online"
)

// CgroupManager manages cgroup v2 resources for jailed VMs.
type CgroupManager struct {
	basePath string // /sys/fs/cgroup/swarmcracker
	logger   zerolog.Logger

	// cpuOnlinePath/nodeOnlinePath default to the /sys files above and are
	// overridable in tests.
	cpuOnlinePath  string
	nodeOnlinePath string
}

// ResourceLimits defines CPU/memory/IO constraints for a VM.
type ResourceLimits struct {
	// CPU quota in microseconds (period is typically 1000000 = 1 second)
	// Example: 500000 = 0.5 CPU cores
	CPUQuotaUs int64 `yaml:"cpu_quota_us"`

	// CPU max in format "quota period" (cgroup v2)
	// Example: "500000 1000000" = 0.5 CPU cores
	CPUMax string `yaml:"cpu_max"`

	// Memory limit in bytes
	// Example: 536870912 = 512MB
	MemoryMax int64 `yaml:"memory_max"`

	// Memory throttle threshold (soft limit)
	// VM will be throttled before hitting hard limit
	MemoryHigh int64 `yaml:"memory_high"`

	// IO weight (1-10000, default 100)
	// Higher = more IO bandwidth
	IOWeight int `yaml:"io_weight"`

	// IO read bandwidth limit in bytes per second
	IOReadBPS int64 `yaml:"io_read_bps"`

	// IO write bandwidth limit in bytes per second
	IOWriteBPS int64 `yaml:"io_write_bps"`

	// CpusetCPUs pins the VM to this CPU list (Linux cpulist syntax,
	// canonicalised by ParseCPUSet). Empty means "do not pin".
	CpusetCPUs string `yaml:"cpuset_cpus"`

	// CpusetMems seeds cpuset.mems. Empty means "inherit the parent's
	// cpuset.mems, then the host's online NUMA nodes".
	CpusetMems string `yaml:"cpuset_mems"`
}

// NewCgroupManager creates a new cgroup manager.
func NewCgroupManager(basePath string) (*CgroupManager, error) {
	if basePath == "" {
		basePath = "/sys/fs/cgroup/swarmcracker"
	}

	// Verify cgroup v2 is available
	if !isCgroupV2Available() {
		return nil, fmt.Errorf("cgroup v2 not available on this system")
	}

	// Create base directory
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cgroup base dir: %w", err)
	}

	return &CgroupManager{
		basePath:       basePath,
		logger:         log.With().Str("component", "cgroup-manager").Logger(),
		cpuOnlinePath:  defaultCPUOnlinePath,
		nodeOnlinePath: defaultNodeOnlinePath,
	}, nil
}

// EnsureControllers enables cgroup controllers for child cgroups: it delegates
// the requested controllers in the parent's cgroup.subtree_control and then
// enables them in the manager's base path.
//
// It is additive and idempotent: a controller that is already enabled is left
// alone, nothing is ever removed, and a controller the kernel does not
// advertise is never written. Callers enable controllers so that cpuset, cpu,
// memory, and io are actually usable in the per-task child cgroups; without
// this, v2 only lists them in the cgroup's file set.
//
// When one or more controllers cannot be enabled it returns a
// *ControllersError naming each one with the reason (the usual real cause is
// that the service's own cgroup is not delegated, i.e. systemd's Delegate=yes
// is missing).
func (m *CgroupManager) EnsureControllers(controllers ...string) error {
	if len(controllers) == 0 {
		return nil
	}

	requested, err := normalizeControllers(controllers)
	if err != nil {
		return err
	}

	// The base must be a real cgroup directory; without cgroup.controllers the
	// hierarchy cannot be reasoned about at all.
	if _, err := readControllerSet(filepath.Join(m.basePath, "cgroup.controllers")); err != nil {
		return fmt.Errorf("failed to read cgroup.controllers for %s: %w", m.basePath, err)
	}
	baseEnabled, err := readControllerSet(filepath.Join(m.basePath, "cgroup.subtree_control"))
	if err != nil {
		return fmt.Errorf("failed to read cgroup.subtree_control for %s: %w", m.basePath, err)
	}

	// Availability is decided by the parent: a controller the parent does not
	// advertise can never be delegated down to the base, and writing it would
	// be a silent no-op at best. Anything already enabled at the base is done.
	parent := filepath.Dir(m.basePath)
	parentAdvertised, err := readControllerSet(filepath.Join(parent, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("failed to read cgroup.controllers for parent %s: %w", parent, err)
	}

	errs := &ControllersError{Requested: requested, Failures: map[string]string{}}
	toEnable := make([]string, 0, len(requested))
	for _, c := range requested {
		if _, ok := baseEnabled[c]; ok {
			continue
		}
		if _, ok := parentAdvertised[c]; ok {
			toEnable = append(toEnable, c)
			continue
		}
		errs.Unavailable = append(errs.Unavailable, c)
	}
	if len(toEnable) == 0 {
		return errsOrNil(errs)
	}

	// The parent must delegate first; only then does the base's own
	// cgroup.controllers advertise the controller and accept it.
	if err := enableControllersAt(parent, toEnable); err != nil {
		for _, c := range toEnable {
			errs.Failures[c] = fmt.Sprintf("cannot delegate through parent %s: %v", parent, err)
		}
		return errs
	}

	if err := enableControllersAt(m.basePath, toEnable); err != nil {
		for _, c := range toEnable {
			errs.Failures[c] = fmt.Sprintf("cannot enable at %s: %v", m.basePath, err)
		}
		return errs
	}

	// Verify against the kernel's view rather than trusting the write.
	if now, err := readControllerSet(filepath.Join(m.basePath, "cgroup.subtree_control")); err == nil {
		for _, c := range toEnable {
			if _, ok := now[c]; !ok {
				errs.Failures[c] = fmt.Sprintf("not enabled in %s after write", filepath.Join(m.basePath, "cgroup.subtree_control"))
			}
		}
	}

	return errsOrNil(errs)
}

// errsOrNil returns errs when it carries any problem and nil otherwise.
func errsOrNil(errs *ControllersError) error {
	if len(errs.Failures) > 0 || len(errs.Unavailable) > 0 {
		return errs
	}
	return nil
}

// ControllersError reports controllers that EnsureControllers could not enable.
type ControllersError struct {
	// Requested is the deduplicated controller list the caller asked for.
	Requested []string
	// Unavailable lists controllers the kernel does not advertise in the
	// parent's cgroup.controllers, so they cannot be delegated to the base.
	Unavailable []string
	// Failures maps a controller to why enabling it failed.
	Failures map[string]string
}

// Error implements error.
func (e *ControllersError) Error() string {
	reasons := make([]string, 0, len(e.Unavailable)+len(e.Failures))
	if len(e.Unavailable) > 0 {
		reasons = append(reasons, "not advertised by the kernel: "+strings.Join(e.Unavailable, ", "))
	}
	keys := make([]string, 0, len(e.Failures))
	for c := range e.Failures {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		reasons = append(reasons, fmt.Sprintf("%s: %s", c, e.Failures[c]))
	}
	return fmt.Sprintf("failed to enable cgroup controllers [%s]: %s",
		strings.Join(e.Requested, " "), strings.Join(reasons, "; "))
}

// normalizeControllers validates controller names and removes duplicates while
// preserving the caller's order.
func normalizeControllers(controllers []string) ([]string, error) {
	out := make([]string, 0, len(controllers))
	seen := make(map[string]struct{}, len(controllers))
	for _, c := range controllers {
		if c == "" {
			return nil, fmt.Errorf("invalid cgroup controller name %q: empty", c)
		}
		for i := 0; i < len(c); i++ {
			ch := c[i]
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
				return nil, fmt.Errorf("invalid cgroup controller name %q: want [a-z0-9_]", c)
			}
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

// readControllerSet reads a cgroup.controllers or cgroup.subtree_control file
// into a set. Both files are space-separated controller names on read (the +/-
// prefixes are write-only syntax, but are tolerated here so a fixture that
// stores a raw write is still read correctly).
func readControllerSet(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{})
	for _, name := range strings.Fields(string(data)) {
		set[strings.TrimPrefix(name, "+")] = struct{}{}
	}
	return set, nil
}

// enableControllersAt adds controllers to dir's cgroup.subtree_control without
// disabling anything already enabled there.
func enableControllersAt(dir string, controllers []string) error {
	path := filepath.Join(dir, "cgroup.subtree_control")
	enabled, err := readControllerSet(path)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	toAdd := make([]string, 0, len(controllers))
	for _, c := range controllers {
		if _, ok := enabled[c]; !ok {
			toAdd = append(toAdd, c)
			enabled[c] = struct{}{}
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	// Rewrite the whole set with "+" prefixes: that is additive on a real
	// cgroupfs (enabling an already-enabled controller is a no-op) and keeps
	// the previously enabled controllers visible in a plain-file fixture.
	tokens := make([]string, 0, len(enabled))
	for c := range enabled {
		tokens = append(tokens, "+"+c)
	}
	sort.Strings(tokens)

	line := strings.Join(tokens, " ")
	if err := os.WriteFile(path, []byte(line), 0644); err != nil {
		return fmt.Errorf("failed to write %q to %s: %w", line, path, err)
	}
	return nil
}

// validateResourceLimits rejects limit values that CreateCgroup would otherwise
// accept and silently skip. Zero means "unset" everywhere; a negative value is
// never meaningful.
func validateResourceLimits(limits ResourceLimits) error {
	if limits.IOWeight < 0 {
		return fmt.Errorf("invalid IOWeight %d: must be non-negative (0 means unset)", limits.IOWeight)
	}
	return nil
}

// CgroupStage identifies the step of a cgroup operation that failed.
type CgroupStage string

const (
	// CgroupStageValidate rejects unhonourable limits before anything is created.
	CgroupStageValidate CgroupStage = "validate"
	// CgroupStageCreateDir creates the per-task cgroup directory.
	CgroupStageCreateDir CgroupStage = "create-dir"
	// CgroupStageCpuset writes cpuset.cpus or cpuset.mems.
	CgroupStageCpuset CgroupStage = "cpuset"
	// CgroupStageCPU writes cpu.max.
	CgroupStageCPU CgroupStage = "cpu"
	// CgroupStageMemory writes memory.max or memory.high.
	CgroupStageMemory CgroupStage = "memory"
	// CgroupStageIO writes io.weight or io.max.
	CgroupStageIO CgroupStage = "io"
	// CgroupStageAttach writes the VMM process to cgroup.procs.
	CgroupStageAttach CgroupStage = "attach"
)

// CgroupError reports which stage of a per-task cgroup operation failed and,
// when the failing step targets a single controller file, which field it was. It
// exists so a caller can tell a cpuset that could not be applied (fatal) from a
// quota that could not be applied (a warning) without matching error text.
type CgroupError struct {
	// TaskID is the task whose cgroup was being configured.
	TaskID string
	// Stage is the step that failed.
	Stage CgroupStage
	// Field is the controller file being written, e.g. "cpuset.cpus", when the
	// failing step targets a single file. It is empty otherwise.
	Field string
	// hostHinted marks an Err that already names the host list it failed
	// against (the cpuset writers do). Callers that add their own host hint
	// check it so the hint is not repeated.
	hostHinted bool
	// Err is the underlying failure.
	Err error
}

// Error implements error.
func (e *CgroupError) Error() string {
	if e.TaskID != "" {
		return "task " + e.TaskID + ": " + e.detail()
	}
	return e.detail()
}

// detail renders the stage, its field and the underlying failure without the
// task prefix, for a caller that already names the task in its own message.
func (e *CgroupError) detail() string {
	what := fmt.Sprintf("cgroup %s stage", e.Stage)
	if e.Field != "" {
		what += " (" + e.Field + ")"
	}
	return fmt.Sprintf("%s: %v", what, e.Err)
}

// Unwrap returns the underlying failure.
func (e *CgroupError) Unwrap() error { return e.Err }

// IsQuotaStage reports whether err is a *CgroupError from a quota stage (cpu,
// memory or io). By the time a quota write runs the cpuset has already been
// applied, so a failure there leaves behind a usable, pinned cgroup.
func IsQuotaStage(err error) bool {
	var cgroupErr *CgroupError
	if !errors.As(err, &cgroupErr) {
		return false
	}
	switch cgroupErr.Stage {
	case CgroupStageCPU, CgroupStageMemory, CgroupStageIO:
		return true
	}
	return false
}

// stageError attributes err to stage and taskID. A failure already typed for
// the same stage is returned as-is (with the task filled in), so the stage is
// never repeated when it is wrapped on the way out of CreateCgroup.
func stageError(taskID string, stage CgroupStage, err error) *CgroupError {
	var inner *CgroupError
	if errors.As(err, &inner) && inner.Stage == stage {
		if inner.TaskID == "" {
			inner.TaskID = taskID
		}
		return inner
	}
	e := &CgroupError{TaskID: taskID, Stage: stage, Err: err}
	if errors.As(err, &inner) && inner.Field != "" {
		e.Field = inner.Field
		e.hostHinted = inner.hostHinted
	}
	return e
}

// CreateCgroup creates a cgroup with resource limits for a task.
func (m *CgroupManager) CreateCgroup(taskID string, limits ResourceLimits) error {
	m.logger.Info().
		Str("task_id", taskID).
		Msg("Creating cgroup")

	// Reject unhonourable limits before creating anything, so a bad request
	// never leaves a partially configured cgroup behind.
	if err := validateResourceLimits(limits); err != nil {
		return stageError(taskID, CgroupStageValidate, err)
	}

	// A NUMA request is a hard promise, but the kernel does not enforce it: an
	// offline or parent-excluded node stays in cpuset.mems verbatim while the
	// effective mems silently narrows to the online ∩ parent intersection. Prove
	// every requested node is grantable here, before the cgroup exists.
	if err := m.validateNumaGrant(limits.CpusetMems); err != nil {
		return stageError(taskID, CgroupStageValidate, err)
	}

	cgroupPath := filepath.Join(m.basePath, taskID)

	// Create cgroup directory
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return stageError(taskID, CgroupStageCreateDir, fmt.Errorf("failed to create cgroup dir: %w", err))
	}

	// Set the cpuset first: cpuset.mems must be populated before any process
	// is added (AddProcess writes cgroup.procs), otherwise the kernel refuses
	// the process with ENOSPC.
	if err := m.setCpusetLimits(cgroupPath, limits); err != nil {
		return stageError(taskID, CgroupStageCpuset, err)
	}

	// Set CPU limits
	if err := m.setCPULimits(cgroupPath, limits); err != nil {
		return stageError(taskID, CgroupStageCPU, err)
	}

	// Set memory limits
	if err := m.setMemoryLimits(cgroupPath, limits); err != nil {
		return stageError(taskID, CgroupStageMemory, err)
	}

	// Set IO limits
	if err := m.setIOLimits(cgroupPath, limits); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to set IO limits, continuing anyway")
	}

	m.logger.Info().
		Str("task_id", taskID).
		Str("path", cgroupPath).
		Msg("Cgroup created successfully")

	return nil
}

// AddProcess adds a process to the cgroup.
func (m *CgroupManager) AddProcess(taskID string, pid int) error {
	cgroupPath := filepath.Join(m.basePath, taskID)
	procsFile := filepath.Join(cgroupPath, "cgroup.procs")

	m.logger.Debug().
		Str("task_id", taskID).
		Int("pid", pid).
		Msg("Adding process to cgroup")

	// Write PID to cgroup.procs
	data := fmt.Sprintf("%d", pid)
	if err := os.WriteFile(procsFile, []byte(data), 0644); err != nil {
		return &CgroupError{
			TaskID: taskID,
			Stage:  CgroupStageAttach,
			Field:  "cgroup.procs",
			Err:    fmt.Errorf("failed to add process to cgroup: %w", err),
		}
	}

	m.logger.Info().
		Str("task_id", taskID).
		Int("pid", pid).
		Msg("Process added to cgroup")

	return nil
}

// RemoveCgroup removes a cgroup and all its processes.
func (m *CgroupManager) RemoveCgroup(taskID string) error {
	cgroupPath := filepath.Join(m.basePath, taskID)

	m.logger.Info().
		Str("task_id", taskID).
		Msg("Removing cgroup")

	// Move all processes to parent cgroup first
	procsFile := filepath.Join(cgroupPath, "cgroup.procs")
	if data, err := os.ReadFile(procsFile); err == nil {
		pids := strings.TrimSpace(string(data))
		if pids != "" {
			// Move to root cgroup
			rootProcs := "/sys/fs/cgroup/cgroup.procs"
			if err := os.WriteFile(rootProcs, data, 0644); err != nil {
				m.logger.Warn().Err(err).Msg("Failed to move processes to root cgroup")
			}
		}
	}

	// Remove cgroup directory
	if err := os.RemoveAll(cgroupPath); err != nil {
		return fmt.Errorf("failed to remove cgroup dir: %w", err)
	}

	m.logger.Info().
		Str("task_id", taskID).
		Msg("Cgroup removed successfully")

	return nil
}

// GetStats returns cgroup statistics for a task.
func (m *CgroupManager) GetStats(taskID string) (*CgroupStats, error) {
	cgroupPath := filepath.Join(m.basePath, taskID)

	stats := &CgroupStats{}

	// Read CPU stats
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "cpu.stat")); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				switch fields[0] {
				case "usage_usec":
					stats.CPUUsageUs, _ = strconv.ParseInt(fields[1], 10, 64)
				case "nr_periods":
					stats.CPUPeriods, _ = strconv.ParseInt(fields[1], 10, 64)
				case "nr_throttled":
					stats.CPUThrottled, _ = strconv.ParseInt(fields[1], 10, 64)
				}
			}
		}
	}

	// Read memory stats
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.stat")); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				switch fields[0] {
				case "anon":
					stats.MemoryAnon, _ = strconv.ParseInt(fields[1], 10, 64)
				case "file":
					stats.MemoryFile, _ = strconv.ParseInt(fields[1], 10, 64)
				}
			}
		}
	}

	// Read current memory usage
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.current")); err == nil {
		stats.MemoryCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}

	// Read memory max
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.max")); err == nil {
		val := strings.TrimSpace(string(data))
		if val != "max" {
			stats.MemoryMax, _ = strconv.ParseInt(val, 10, 64)
		} else {
			stats.MemoryMax = -1 // unlimited
		}
	}

	return stats, nil
}

// CgroupStats holds cgroup statistics.
type CgroupStats struct {
	CPUUsageUs    int64 `json:"cpu_usage_us"`
	CPUPeriods    int64 `json:"cpu_periods"`
	CPUThrottled  int64 `json:"cpu_throttled"`
	MemoryAnon    int64 `json:"memory_anon"`
	MemoryFile    int64 `json:"memory_file"`
	MemoryCurrent int64 `json:"memory_current"`
	MemoryMax     int64 `json:"memory_max"`
	IOReadBytes   int64 `json:"io_read_bytes"`
	IOWriteBytes  int64 `json:"io_write_bytes"`
}

// OnlineCPUs returns the canonical list of CPUs the host has online, read from
// /sys/devices/system/cpu/online. It is used to explain cpuset rejections.
func (m *CgroupManager) OnlineCPUs() (string, error) {
	path := m.cpuOnlinePath
	if path == "" {
		path = defaultCPUOnlinePath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read online CPUs from %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", fmt.Errorf("online CPU list at %s is empty", path)
	}
	online, err := ParseCPUSet(raw)
	if err != nil {
		return "", fmt.Errorf("invalid online CPU list at %s: %w", path, err)
	}
	return online, nil
}

// setCpusetLimits writes cpuset.cpus and cpuset.mems for a cgroup.
//
// cpuset.mems is always resolved and written here (never left empty) because a
// cpuset cgroup with an empty mems list refuses processes; AddProcess later
// writes cgroup.procs, so mems must already be in place when CreateCgroup
// returns.
func (m *CgroupManager) setCpusetLimits(cgroupPath string, limits ResourceLimits) error {
	if limits.CpusetCPUs == "" && limits.CpusetMems == "" {
		return nil
	}

	// Resolve and validate both values before writing either: a request that
	// cannot be honoured must not leave a half-configured cgroup behind.
	cpus := ""
	if limits.CpusetCPUs != "" {
		var err error
		cpus, err = ParseCPUSet(limits.CpusetCPUs)
		if err != nil {
			return &CgroupError{
				Stage: CgroupStageCpuset,
				Field: "cpuset.cpus",
				Err:   fmt.Errorf("invalid CpusetCPUs: %w", err),
			}
		}
	}
	mems, source, err := m.resolveCpusetMems(limits.CpusetMems)
	if err != nil {
		return &CgroupError{Stage: CgroupStageCpuset, Field: "cpuset.mems", Err: err}
	}

	if cpus != "" {
		if err := m.writeCpusetFile(cgroupPath, "cpuset.cpus", cpus, "CpusetCPUs"); err != nil {
			return err
		}
	}
	return m.writeCpusetFile(cgroupPath, "cpuset.mems", mems, source)
}

// resolveCpusetMems returns the cpuset.mems value for a new cgroup and a
// human-readable description of where it came from.
//
// Precedence: an explicit CpusetMems, then cpuset.mems of the base path (the
// new cgroup's parent), then the base path's parent (usually the cgroup root),
// then the host's online NUMA-node list. Unreadable or malformed ancestor
// values are skipped in favour of the next source; if every source fails the
// error says so instead of leaving mems empty.
func (m *CgroupManager) resolveCpusetMems(requested string) (string, string, error) {
	if requested != "" {
		mems, err := ParseCPUSet(requested)
		if err != nil {
			return "", "", fmt.Errorf("invalid CpusetMems: %w", err)
		}
		return mems, "CpusetMems", nil
	}

	for _, dir := range []string{m.basePath, filepath.Dir(m.basePath)} {
		path := filepath.Join(dir, "cpuset.mems")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(string(data))
		if raw == "" {
			continue
		}
		mems, err := ParseCPUSet(raw)
		if err != nil {
			m.logger.Warn().Err(err).Str("path", path).Msg("Ignoring malformed ancestor cpuset.mems")
			continue
		}
		return mems, "parent " + path, nil
	}

	nodePath := m.nodeOnlinePath
	if nodePath == "" {
		nodePath = defaultNodeOnlinePath
	}
	data, err := os.ReadFile(nodePath)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve cpuset.mems: CpusetMems is empty, no ancestor cpuset.mems under %s is readable, and the host node list %s is unreadable: %w", m.basePath, nodePath, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", "", fmt.Errorf("cannot resolve cpuset.mems: CpusetMems is empty and the host node list %s is empty", nodePath)
	}
	mems, err := ParseCPUSet(raw)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve cpuset.mems: host node list at %s is invalid: %w", nodePath, err)
	}
	return mems, "host " + nodePath, nil
}

// writeCpusetFile writes one cpuset controller file, wrapping a kernel
// rejection with the requested value, its source, and the host list it
// collided with so the error explains the request. The failing file is named by
// the wrapping *CgroupError's Field; the host axis differs per file: CPUs for
// cpuset.cpus, NUMA memory nodes for cpuset.mems.
func (m *CgroupManager) writeCpusetFile(cgroupPath, name, value, source string) error {
	path := filepath.Join(cgroupPath, name)
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		axis, online := "online CPUs", ""
		if name == "cpuset.mems" {
			axis = "online memory nodes"
			online = m.onlineMemoryNodes()
		} else {
			cpuList, onlineErr := m.OnlineCPUs()
			if onlineErr != nil {
				cpuList = "unknown"
			}
			online = cpuList
		}
		return &CgroupError{
			Stage:      CgroupStageCpuset,
			Field:      name,
			hostHinted: true,
			Err:        fmt.Errorf("failed to write %q (source: %s; %s: %s): %w", value, source, axis, online, err),
		}
	}
	return nil
}

// onlineNodeList returns the host's online NUMA-node list, read from
// nodeOnlinePath (default /sys/devices/system/node/online). Unlike
// onlineMemoryNodes it reports why the list is unavailable, because a NUMA
// grantability check cannot be answered from an unreadable host list.
func (m *CgroupManager) onlineNodeList() (string, error) {
	path := m.nodeOnlinePath
	if path == "" {
		path = defaultNodeOnlinePath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read online NUMA nodes from %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return "", fmt.Errorf("online NUMA node list at %s is empty", path)
	}
	return raw, nil
}

// onlineMemoryNodes reports the host's online NUMA nodes for an error hint. Like
// OnlineCPUs it is best-effort: an unreadable list says "unknown" rather than
// masking the write failure.
func (m *CgroupManager) onlineMemoryNodes() string {
	raw, err := m.onlineNodeList()
	if err != nil {
		return "unknown"
	}
	return raw
}

// setCPULimits configures CPU resource limits.
func (m *CgroupManager) setCPULimits(cgroupPath string, limits ResourceLimits) error {
	// Set CPU max (quota period)
	cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
	cpuMax := limits.CPUMax
	if cpuMax == "" && limits.CPUQuotaUs > 0 {
		// Default period is 1000000 (1 second)
		cpuMax = fmt.Sprintf("%d 1000000", limits.CPUQuotaUs)
	}

	if cpuMax != "" {
		m.logger.Debug().
			Str("cpu_max", cpuMax).
			Msg("Setting CPU limits")
		if err := os.WriteFile(cpuMaxPath, []byte(cpuMax), 0644); err != nil {
			return fmt.Errorf("failed to write cpu.max: %w", err)
		}
	}

	return nil
}

// setMemoryLimits configures memory resource limits.
func (m *CgroupManager) setMemoryLimits(cgroupPath string, limits ResourceLimits) error {
	// Set memory max (hard limit)
	if limits.MemoryMax > 0 {
		memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
		m.logger.Debug().
			Int64("memory_max", limits.MemoryMax).
			Msg("Setting memory max limit")
		if err := os.WriteFile(memoryMaxPath, []byte(fmt.Sprintf("%d", limits.MemoryMax)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.max: %w", err)
		}
	}

	// Set memory high (soft limit/throttle threshold)
	if limits.MemoryHigh > 0 {
		memoryHighPath := filepath.Join(cgroupPath, "memory.high")
		m.logger.Debug().
			Int64("memory_high", limits.MemoryHigh).
			Msg("Setting memory high threshold")
		if err := os.WriteFile(memoryHighPath, []byte(fmt.Sprintf("%d", limits.MemoryHigh)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.high: %w", err)
		}
	}

	return nil
}

// setIOLimits configures IO resource limits.
func (m *CgroupManager) setIOLimits(cgroupPath string, limits ResourceLimits) error {
	// Set IO weight. The cgroup v2 format is "default <weight>"; a bare number
	// is rejected by the kernel. Zero means "unset" here; CreateCgroup rejects
	// negative weights up front, so this gate never silently drops a value.
	if limits.IOWeight > 0 {
		ioWeightPath := filepath.Join(cgroupPath, "io.weight")
		weight := fmt.Sprintf("default %d", limits.IOWeight)
		m.logger.Debug().
			Int("io_weight", limits.IOWeight).
			Msg("Setting IO weight")
		if err := os.WriteFile(ioWeightPath, []byte(weight), 0644); err != nil {
			return fmt.Errorf("failed to write io.weight: %w", err)
		}
	}

	// Set IO bandwidth limits (requires device major:minor)
	if limits.IOReadBPS > 0 || limits.IOWriteBPS > 0 {
		// Discover block devices backing the rootfs or data drives
		devices, err := m.discoverBlockDevices()
		if err != nil {
			m.logger.Warn().Err(err).Msg("Failed to discover block devices, skipping IO bandwidth limits")
			return nil // Non-fatal: weight is still applied
		}

		if len(devices) == 0 {
			m.logger.Debug().Msg("No block devices found for IO limits")
			return nil
		}

		// Apply IO bandwidth limits to each device
		ioMaxPath := filepath.Join(cgroupPath, "io.max")
		for _, dev := range devices {
			line := fmt.Sprintf("%d:%d rbps=%d wbps=%d",
				dev.Major, dev.Minor, limits.IOReadBPS, limits.IOWriteBPS)
			m.logger.Debug().
				Int("major", dev.Major).
				Int("minor", dev.Minor).
				Int64("rbps", limits.IOReadBPS).
				Int64("wbps", limits.IOWriteBPS).
				Msg("Setting io.max for device")
			if err := os.WriteFile(ioMaxPath, []byte(line), 0644); err != nil {
				return fmt.Errorf("failed to write io.max for device %d:%d: %w", dev.Major, dev.Minor, err)
			}
		}
	}

	return nil
}

// BlockDevice represents a block device with its major:minor numbers.
type BlockDevice struct {
	Major int
	Minor int
	Path  string
}

// discoverBlockDevices discovers the block devices backing the VM storage.
// Returns a list of devices with their major:minor numbers.
func (m *CgroupManager) discoverBlockDevices() ([]BlockDevice, error) {
	// Strategy: find devices from common VM storage locations
	// 1. Check /var/lib/firecracker (typical rootfs location)
	// 2. Check /var/lib/swarmcracker (state dir)
	// 3. Fallback to root filesystem if specific paths not found

	devices := []BlockDevice{}

	// Check common VM storage paths
	storagePaths := []string{
		"/var/lib/firecracker",
		"/var/lib/swarmcracker",
		"/var/lib/docker", // Docker volumes
		"/",               // Root filesystem as fallback
	}

	for _, path := range storagePaths {
		dev, err := m.getDeviceForPath(path)
		if err != nil {
			m.logger.Debug().Str("path", path).Err(err).Msg("Could not get device for path")
			continue
		}

		// Avoid duplicates
		found := false
		for _, existing := range devices {
			if existing.Major == dev.Major && existing.Minor == dev.Minor {
				found = true
				break
			}
		}
		if !found {
			devices = append(devices, dev)
		}
	}

	return devices, nil
}

// getDeviceForPath returns the block device backing a given path.
func (m *CgroupManager) getDeviceForPath(path string) (BlockDevice, error) {
	// Use stat to get device ID
	stat, err := os.Stat(path)
	if err != nil {
		return BlockDevice{}, fmt.Errorf("failed to stat path: %w", err)
	}

	// Get device number from stat
	st, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return BlockDevice{}, fmt.Errorf("unexpected stat type for path %s", path)
	}
	dev := st.Dev

	// Extract major and minor numbers
	major := int(dev >> 8)
	minor := int(dev & 0xff)

	// Verify by reading from /sys/dev/block/
	devPath := fmt.Sprintf("/sys/dev/block/%d:%d/dev", major, minor)
	if _, err := os.Stat(devPath); err != nil {
		// Try alternate calculation for some systems
		major = int((dev >> 20) & 0xfff)
		minor = int(dev & 0xfffff)
		devPath = fmt.Sprintf("/sys/dev/block/%d:%d/dev", major, minor)
		if _, err := os.Stat(devPath); err != nil {
			return BlockDevice{}, fmt.Errorf("device %d:%d not found in /sys", major, minor)
		}
	}

	return BlockDevice{
		Major: major,
		Minor: minor,
		Path:  path,
	}, nil
}
func isCgroupV2Available() bool {
	// Check if cgroup2 filesystem is mounted
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return true
	}

	// Alternative check: look for cgroup2 mount
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "cgroup2") {
			return true
		}
	}

	return false
}

// DetectCgroupVersion returns the cgroup version in use.
func DetectCgroupVersion() string {
	if isCgroupV2Available() {
		return "v2"
	}
	return "v1"
}
