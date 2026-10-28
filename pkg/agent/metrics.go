package agent

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is the filesystem guest metrics are read from. It is a variable so a
// test can point the readers at a fixture tree; the guest agent always reads the
// real /proc.
var procRoot = "/proc"

// GuestMetrics is the resource use a guest reports about itself. Every field is
// cumulative or an instantaneous level read from the guest's own /proc, so the
// host never has to infer a VM's usage from the Firecracker process it can see.
type GuestMetrics struct {
	// CPUSeconds is the guest's busy CPU time, summed over all vCPUs and
	// excluding idle and iowait. It is cumulative since guest boot.
	CPUSeconds float64 `json:"cpu_seconds"`
	// MemoryBytes is the guest memory in use: MemTotal minus MemAvailable.
	MemoryBytes uint64 `json:"memory_bytes"`
	// NetRxBytes and NetTxBytes are cumulative byte counters summed over the
	// guest's network interfaces, excluding loopback.
	NetRxBytes uint64 `json:"net_rx_bytes"`
	NetTxBytes uint64 `json:"net_tx_bytes"`
}

// ReadGuestMetrics samples the guest's /proc under root.
//
// It fails rather than reporting zeros when a file it needs is unreadable: a
// zero sample is indistinguishable from an idle guest and would silently pin a
// per-task CPU or network series at 0.
func ReadGuestMetrics(root string) (GuestMetrics, error) {
	cpu, err := readCPUSeconds(filepath.Join(root, "stat"))
	if err != nil {
		return GuestMetrics{}, err
	}
	mem, err := readMemoryBytes(filepath.Join(root, "meminfo"))
	if err != nil {
		return GuestMetrics{}, err
	}
	rx, tx, err := readNetworkBytes(filepath.Join(root, "net", "dev"))
	if err != nil {
		return GuestMetrics{}, err
	}
	return GuestMetrics{CPUSeconds: cpu, MemoryBytes: mem, NetRxBytes: rx, NetTxBytes: tx}, nil
}

// readCPUSeconds sums the busy fields of the aggregate "cpu" line of /proc/stat.
//
// The columns after "cpu" are: user nice system idle iowait irq softirq steal
// guest guest_nice. Idle and iowait are excluded, and guest/guest_nice are not
// added because the kernel already counts them in user/nice. The result is
// therefore the time the guest actually spent running, not wall-clock uptime.
func readCPUSeconds(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("read cpu seconds: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}
		return sumBusyTicks(fields[1:])
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read cpu seconds: %w", err)
	}
	return 0, fmt.Errorf("read cpu seconds: %s has no aggregate cpu line", path)
}

// busyTickColumns maps the positions of the aggregate "cpu" line's counters to
// the ones that count as work: user, nice, system, irq, softirq and steal.
var busyTickColumns = []int{0, 1, 2, 5, 6, 7}

// sumBusyTicks converts the busy counters of an aggregate "cpu" line to seconds.
func sumBusyTicks(counters []string) (float64, error) {
	var ticks uint64
	for _, column := range busyTickColumns {
		if column >= len(counters) {
			// An old or truncated kernel reports fewer columns; the ones it
			// does report are still valid, so sum what is there.
			continue
		}
		value, err := strconv.ParseUint(counters[column], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse cpu counter %q: %w", counters[column], err)
		}
		ticks += value
	}
	return float64(ticks) / clockTicksPerSecond, nil
}

// clockTicksPerSecond is the unit of the counters in /proc/stat on every Linux
// configuration Go supports for the guest (USER_HZ is 100).
const clockTicksPerSecond = 100

// readMemoryBytes returns the guest's memory in use from /proc/meminfo:
// MemTotal minus MemAvailable, the kernel's own estimate of what a new workload
// could claim without swapping. Kernels older than 3.14 have no MemAvailable;
// there it falls back to MemFree+Buffers+Cached, which excludes reclaimable slab
// and overstates usage slightly.
func readMemoryBytes(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("read memory bytes: %w", err)
	}
	defer func() { _ = f.Close() }()

	values := make(map[string]uint64, 8)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseMeminfoLine(scanner.Text())
		if !ok {
			continue
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read memory bytes: %w", err)
	}

	total, ok := values["MemTotal"]
	if !ok {
		return 0, fmt.Errorf("read memory bytes: %s has no MemTotal", path)
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	if available > total {
		// A truncated read or a bogus fixture; never report a negative use.
		return 0, nil
	}
	return (total - available) * 1024, nil
}

// parseMeminfoLine parses one "Key: <value> kB" line of /proc/meminfo,
// returning the value in kB.
func parseMeminfoLine(line string) (key string, value uint64, ok bool) {
	name, rest, found := strings.Cut(line, ":")
	if !found {
		return "", 0, false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", 0, false
	}
	parsed, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return name, parsed, true
}

// readNetworkBytes sums the receive and transmit byte counters of /proc/net/dev
// over every interface except loopback.
//
// Loopback is excluded on purpose: traffic between processes in the same guest
// never leaves the VM, so counting it would report network use the guest did not
// have. The counters are cumulative since the interface was created.
func readNetworkBytes(path string) (rx, tx uint64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read network bytes: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	sawInterface := false
	for scanner.Scan() {
		line := scanner.Text()
		name, rest, found := strings.Cut(line, ":")
		if !found {
			// The two header lines have no colon.
			continue
		}
		iface := strings.TrimSpace(name)
		if iface == "" || iface == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		// struct rtnl_link_stats: 8 receive counters then 8 transmit counters,
		// the first of each being the byte count.
		if len(fields) < 9 {
			continue
		}
		rxBytes, rxErr := strconv.ParseUint(fields[0], 10, 64)
		txBytes, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		sawInterface = true
		rx += rxBytes
		tx += txBytes
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("read network bytes: %w", err)
	}
	if !sawInterface {
		// Zero traffic is a legitimate reading; no interface at all is not, and
		// would pin the network series at 0 forever.
		return 0, 0, fmt.Errorf("read network bytes: %s lists no non-loopback interface", path)
	}
	return rx, tx, nil
}
