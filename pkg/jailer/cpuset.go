package jailer

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// cpusetRange is an inclusive range of CPU or NUMA-node IDs.
type cpusetRange struct {
	start int64
	end   int64
}

// ParseCPUSet validates a Linux cpulist (the syntax used by cpuset.cpus,
// cpuset.mems, and /sys/devices/system/cpu/online) and returns its canonical
// form: ranges sorted in ascending order with overlapping and adjacent ranges
// merged, and no leading zeros.
//
//	"3,1-2"   -> "1-3"
//	"0-3,2-5" -> "0-5"
//	"7"       -> "7"
//
// Anything that is not a well-formed list is rejected: an empty string,
// whitespace, a bare word ("a"), a dangling dash ("1-"), a negative number
// ("-1"), a reversed range ("5-3"), an empty element ("1,,2"), or a
// zero-padded number ("01"). Rejecting rather than repairing keeps a typo from
// silently pinning a VM to an unintended CPU.
func ParseCPUSet(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("invalid cpuset %q: empty", s)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("invalid cpuset %q: whitespace-only", s)
	}
	if strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("invalid cpuset %q: must not contain whitespace", s)
	}

	parts := strings.Split(s, ",")
	ranges := make([]cpusetRange, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("invalid cpuset %q: empty list element", s)
		}

		lo, hi, isRange := strings.Cut(part, "-")
		start, err := parseCPUSetID(lo, s, part)
		if err != nil {
			return "", err
		}
		end := start
		if isRange {
			if hi == "" {
				return "", fmt.Errorf("invalid cpuset %q: element %q has a trailing '-', want N or N-M", s, part)
			}
			end, err = parseCPUSetID(hi, s, part)
			if err != nil {
				return "", err
			}
			if end < start {
				return "", fmt.Errorf("invalid cpuset %q: element %q is reversed, want ascending N-M", s, part)
			}
		}
		ranges = append(ranges, cpusetRange{start: start, end: end})
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})

	merged := make([]cpusetRange, 0, len(ranges))
	for _, r := range ranges {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			adjacent := last.end < math.MaxInt64 && r.start == last.end+1
			if r.start <= last.end || adjacent {
				if r.end > last.end {
					last.end = r.end
				}
				continue
			}
		}
		merged = append(merged, r)
	}

	var b strings.Builder
	for i, r := range merged {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(r.start, 10))
		if r.end != r.start {
			b.WriteByte('-')
			b.WriteString(strconv.FormatInt(r.end, 10))
		}
	}
	return b.String(), nil
}

// parseCPUSetID parses one decimal CPU/node ID out of a cpulist element.
func parseCPUSetID(tok, whole, element string) (int64, error) {
	if tok == "" {
		return 0, fmt.Errorf("invalid cpuset %q: element %q is missing a number", whole, element)
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, fmt.Errorf("invalid cpuset %q: element %q is not a decimal number", whole, element)
		}
	}
	if len(tok) > 1 && tok[0] == '0' {
		return 0, fmt.Errorf("invalid cpuset %q: element %q is zero-padded", whole, element)
	}

	id, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpuset %q: element %q is out of range", whole, element)
	}
	return id, nil
}
