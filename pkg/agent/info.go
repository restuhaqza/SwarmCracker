package agent

// The `info` operation: what the guest knows about itself.
//
// A template that ships neither a shell nor coreutils still has a kernel, a
// machine id, an amount of memory and a set of interpreters and package tools
// baked into it. The node needs to know which ones before it schedules anything
// there, and asking over `exec` means the answer depends on the very things that
// may be missing. `info` therefore reads the guest's own facts itself: /proc for
// the running kernel and machine, /etc for the identity, and a fixed list of
// directories for the toolchain.
//
// The toolchain probe is a fixed list of names looked up in a fixed list of
// directories, never an arbitrary search: a capability that reports "whatever is
// installed" cannot be bounded, and an operation whose cost depends on the
// caller's request is an operation a caller can make expensive.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// GuestInfo is what a guest reports about itself: enough for a scheduler to
// decide whether this image can run a task, and enough for an operator to know
// which guest answered.
type GuestInfo struct {
	// Hostname and MachineID are the guest's identity. MachineID is the value
	// `prepare` writes, so two clones of one checkpoint differ here.
	Hostname  string `json:"hostname"`
	MachineID string `json:"machine_id"`
	// KernelRelease ("6.1.0") and KernelVersion are the guest's own kernel, read
	// from /proc rather than from the host's kernel.
	KernelRelease string `json:"kernel_release"`
	KernelVersion string `json:"kernel_version"`
	// Machine is the guest's architecture as the kernel names it ("x86_64",
	// "aarch64"), which is what a template's manifest is written against.
	Machine string `json:"machine"`
	// OSName and OSID come from /etc/os-release ("Alpine Linux v3.20", "alpine").
	// A template without that file reports both empty rather than a guess.
	OSName string `json:"os_name"`
	OSID   string `json:"os_id"`
	// CPUs and MemoryTotalBytes are the resources the guest believes it has:
	// the vCPUs its kernel counts and the MemTotal of its own /proc/meminfo.
	CPUs             int    `json:"cpus"`
	MemoryTotalBytes uint64 `json:"memory_total_bytes"`
	// UptimeSeconds is how long the guest has been up, from /proc/uptime. It is
	// the guest's own clock, which a restore rewrites (prepare sets it), so it
	// measures the running guest and not the checkpoint.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// Protocol, AgentVersion, BuildID and BootID describe the agent that
	// answered, so a host can log which build it is driving and which boot it is
	// talking to without a second handshake.
	Protocol     string `json:"protocol"`
	AgentVersion string `json:"agent_version"`
	BuildID      string `json:"build_id"`
	BootID       string `json:"boot_id"`
	// Tools are the names from toolProbe the guest's own directories hold, sorted
	// by name so two reads of an unchanged image agree.
	Tools []string `json:"tools"`
}

const (
	// maxInfoFile bounds every file `info` reads. os-release is the largest of
	// them by far and is a few hundred bytes on a real image; the bound exists so
	// a corrupt or hostile file cannot make the agent allocate without limit.
	maxInfoFile = 64 << 10
)

// etcRoot is where the guest's configuration identity is read from, for `info`
// and for `net`'s resolver. It is a variable so a test can point the readers at a
// fixture tree; the guest agent always reads the real /etc. The kernel's own files
// are read through procRoot, which metrics.go already owns.
var etcRoot = "/etc"

// toolProbe is the toolchain `info` looks for: the interpreters and package
// tools a template either has or does not, and the ones the node's own paths
// (exec, tar) need. It is a fixed list so the cost of the operation is fixed.
var toolProbe = []string{
	"sh", "bash", "tar", "gzip", "curl", "wget",
	"python3", "node", "npm", "go", "git", "jq",
}

// toolDirs are the directories the toolchain probe looks in. A workload's PATH
// is not the agent's — the agent is started by the guest's init, not by a login
// shell — so the probe names the standard directories instead of consulting
// PATH. It is a variable so a test can narrow it.
var toolDirs = []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// readGuestInfo reads the guest's identity, kernel, resources and toolchain.
//
// The kernel and resource files are required: a guest whose /proc cannot be read
// is a guest whose answers would be fiction, and reporting "no kernel" is worse
// than reporting a failure. The identity files are optional, because a template
// may legitimately ship neither os-release nor a machine id, and an empty field
// says exactly that.
func readGuestInfo() (GuestInfo, error) {
	release, err := readInfoField(filepath.Join(procRoot, "sys", "kernel", "osrelease"))
	if err != nil {
		return GuestInfo{}, err
	}
	version, err := readInfoField(filepath.Join(procRoot, "sys", "kernel", "version"))
	if err != nil {
		return GuestInfo{}, err
	}
	hostname, err := readInfoField(filepath.Join(procRoot, "sys", "kernel", "hostname"))
	if err != nil {
		return GuestInfo{}, err
	}
	uptime, err := readUptime(filepath.Join(procRoot, "uptime"))
	if err != nil {
		return GuestInfo{}, err
	}
	total, err := readMemTotal(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		return GuestInfo{}, err
	}

	info := GuestInfo{
		Hostname:         hostname,
		MachineID:        readOptionalField(filepath.Join(etcRoot, "machine-id")),
		KernelRelease:    release,
		KernelVersion:    version,
		Machine:          machineArch(),
		CPUs:             cpuCount(),
		MemoryTotalBytes: total,
		UptimeSeconds:    uptime,
		Protocol:         Version,
		AgentVersion:     AgentVersion,
		BuildID:          BuildID,
		BootID:           BootID(),
		Tools:            probeTools(),
	}
	info.OSID, info.OSName = readOSRelease(filepath.Join(etcRoot, "os-release"))
	return info, nil
}

// readInfoField reads a one-value kernel file, trimmed, and fails when it cannot.
func readInfoField(path string) (string, error) {
	data, err := readCappedFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// readOptionalField reads a file whose absence is not a failure, returning the
// empty string when it is missing or unreadable. It is used for the identity
// files a template may simply not ship.
func readOptionalField(path string) string {
	data, err := readCappedFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readCappedFile reads a file of at most maxInfoFile bytes. A file larger than
// the bound is refused rather than truncated: a truncated os-release would report
// a name the image does not have.
func readCappedFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	limited := make([]byte, maxInfoFile+1)
	n, err := f.Read(limited)
	if err != nil && n == 0 {
		return nil, err
	}
	if n > maxInfoFile {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxInfoFile)
	}
	return limited[:n], nil
}

// readUptime returns the guest's uptime in seconds: the first field of
// /proc/uptime.
func readUptime(path string) (float64, error) {
	data, err := readCappedFile(path)
	if err != nil {
		return 0, fmt.Errorf("read uptime: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("read uptime: %s is empty", path)
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("read uptime: parse %q: %w", fields[0], err)
	}
	return seconds, nil
}

// readMemTotal returns the guest's MemTotal in bytes. It reuses the meminfo
// parser metrics.go reads the same file with, so both operations agree about what
// the guest's memory is.
func readMemTotal(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("read memory total: %w", err)
	}
	defer func() { _ = f.Close() }()

	var total uint64
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if !ok || key != "MemTotal" {
			continue
		}
		total, found = value, true
		break
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read memory total: %w", err)
	}
	if !found {
		return 0, fmt.Errorf("read memory total: %s has no MemTotal", path)
	}
	return total * 1024, nil
}

// readOSRelease parses /etc/os-release into (id, name). The name prefers
// PRETTY_NAME and falls back to NAME, because a template may ship only the
// latter. A missing file is not an error: the identity fields are simply empty.
func readOSRelease(path string) (id, name string) {
	data, err := readCappedFile(path)
	if err != nil {
		return "", ""
	}
	values := make(map[string]string, 8)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[key] = unquoteOSRelease(value)
	}
	name = values["PRETTY_NAME"]
	if name == "" {
		name = values["NAME"]
	}
	return values["ID"], name
}

// unquoteOSRelease removes the single or double quotes os-release values are
// written with, leaving anything else as it was.
func unquoteOSRelease(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// probeTools returns the probe names the guest's own directories hold, sorted. A
// name is present when a regular file or symlink of that name exists in
// one of toolDirs; what it points at is not resolved, because an image that ships
// a dangling interpreter is a broken image and the probe should say so.
func probeTools() []string {
	found := make([]string, 0, len(toolProbe))
	for _, name := range toolProbe {
		for _, dir := range toolDirs {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
				found = append(found, name)
				break
			}
		}
	}
	sort.Strings(found)
	return found
}

// cpuCount is the number of CPUs the agent may run on, which inside a guest is
// the vCPU count the kernel was started with. It is the same number a workload's
// `nproc` reports, because both read the affinity mask.
func cpuCount() int { return runtime.NumCPU() }
