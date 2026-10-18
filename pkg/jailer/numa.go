package jailer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validateNumaGrant checks that every node in an explicit cpuset.mems request is
// actually grantable, because the kernel does not fail an ungrantable one:
//
//   - cgroup v2 keeps the requested value in cpuset.mems verbatim across
//     hotplug, and the effective mems is the parent ∩ online intersection, so a
//     node that is offline (or withheld by an ancestor) silently narrows the
//     placement instead of failing the write;
//   - only a node outside the possible map makes the write fail.
//
// A request that cannot be honoured therefore has to be refused here, before
// anything is written, so the "no silently ignored settings" contract holds for
// NUMA exactly as it does for cpuset.cpus. An empty request returns nil: the
// parent → online auto-resolution is unchanged and is not validated.
//
// The two checks are independent, so a request can fail both; each failing part
// is reported with the request, the host's online nodes, and (when it is what
// excluded the request) the parent's effective set.
func (m *CgroupManager) validateNumaGrant(requested string) error {
	if requested == "" {
		return nil
	}

	want, err := parseCPUSetRanges(requested)
	if err != nil {
		// The syntax error keeps resolveCpusetMems's wording so the rejection
		// reads the same wherever it is caught.
		return fmt.Errorf("invalid CpusetMems: %w", err)
	}
	want = mergeCPUSetRanges(want)
	request := formatCPUSetRanges(want)

	onlineValue, err := m.onlineNodeList()
	if err != nil {
		return fmt.Errorf("cannot verify NUMA nodes %s are grantable: %w", request, err)
	}
	onlineRanges, err := parseCPUSetRanges(onlineValue)
	if err != nil {
		return fmt.Errorf("cannot verify NUMA nodes %s are grantable: invalid host online NUMA node list %q: %w",
			request, onlineValue, err)
	}
	online := mergeCPUSetRanges(onlineRanges)
	onlineList := formatCPUSetRanges(online)

	reasons := make([]string, 0, 2)
	if missing := subtractCPUSetRanges(want, online); len(missing) > 0 {
		reasons = append(reasons, fmt.Sprintf("node(s) %s are not online (host online NUMA nodes: %s)",
			formatCPUSetRanges(missing), onlineList))
	}
	if allowed, value, path, ok := m.parentEffectiveMems(); ok {
		if missing := subtractCPUSetRanges(want, allowed); len(missing) > 0 {
			reasons = append(reasons, fmt.Sprintf("node(s) %s are excluded by the parent cgroup's cpuset.mems.effective %s (%s; host online NUMA nodes: %s)",
				formatCPUSetRanges(missing), value, path, onlineList))
		}
	}
	if len(reasons) == 0 {
		return nil
	}

	return fmt.Errorf("requested NUMA nodes %s are not grantable: %s", request, strings.Join(reasons, "; "))
}

// parentEffectiveMems returns the effective cpuset.mems of the nearest readable
// ancestor of the per-task cgroup, starting at the manager's base path (the new
// cgroup's parent) and walking up. The nearest effective value already is the
// intersection of the whole chain above it, so checking it is enough. ok is
// false when no ancestor exposes a readable, well-formed cpuset.mems.effective:
// in that case the parent side cannot narrow anything observable and is not
// checked.
func (m *CgroupManager) parentEffectiveMems() (allowed []cpusetRange, value, path string, ok bool) {
	if m.basePath == "" {
		return nil, "", "", false
	}

	dir := m.basePath
	for {
		path = filepath.Join(dir, "cpuset.mems.effective")
		if data, err := os.ReadFile(path); err == nil {
			raw := strings.TrimSpace(string(data))
			if raw != "" {
				if ranges, err := parseCPUSetRanges(raw); err == nil {
					return mergeCPUSetRanges(ranges), raw, path, true
				}
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, "", "", false
		}
		dir = parent
	}
}

// subtractCPUSetRanges returns the parts of want that allowed does not cover.
// Both inputs are merged, ascending range lists, as produced by
// mergeCPUSetRanges; the result is ascending, and empty when allowed covers
// want entirely.
func subtractCPUSetRanges(want, allowed []cpusetRange) []cpusetRange {
	missing := make([]cpusetRange, 0, len(want))
	for _, w := range want {
		cur := w.start
		covered := false
		for _, a := range allowed {
			if a.end < cur {
				continue
			}
			if a.start > w.end {
				break
			}
			if a.start > cur {
				missing = append(missing, cpusetRange{start: cur, end: a.start - 1})
			}
			if a.end >= w.end {
				covered = true
				break
			}
			cur = a.end + 1
		}
		if !covered && cur <= w.end {
			missing = append(missing, cpusetRange{start: cur, end: w.end})
		}
	}
	return missing
}
