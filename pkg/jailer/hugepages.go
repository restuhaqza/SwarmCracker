package jailer

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Host hugepage state lives in this package, next to the other host-state
// readers (OnlineCPUs, onlineMemoryNodes), because both VMM start paths
// (pkg/swarmkit and pkg/lifecycle) need it while pkg/types stays
// dependency-free and pkg/translator must not depend on host state.
//
// Firecracker v1.15.1 supports only huge_pages "None" or "2M" and maps guest
// memory with MAP_NORESERVE from the host's pre-allocated hugetlbfs pool. A
// start against a pool that is too small therefore does not fail cleanly: the
// VM faults with SIGBUS later, when the guest touches a page that was never
// allocated. That is why CheckHugepages is a hard precondition, not a warning.

// 2M is the only hugepage size Firecracker supports, so the host pool must
// consist of 2 MiB (2048 KiB) pages and N MiB of guest memory needs N/2 pages.
const (
	hugepageSizeKB2M = 2048
	hugepagesMiB2M   = 2
)

// /proc/meminfo keys that describe the hugepage pool.
const (
	meminfoHugepagesize   = "Hugepagesize"
	meminfoHugePagesTotal = "HugePages_Total"
	meminfoHugePagesFree  = "HugePages_Free"
)

// HugepagesPool is the host's pre-allocated hugepage pool, as reported by
// /proc/meminfo.
type HugepagesPool struct {
	// PageSizeKB is the host's configured hugepage size in KiB (2048 for 2M).
	PageSizeKB int

	// Total is the number of pages currently allocated to the pool.
	Total int

	// Free is the number of allocated pages not yet in use.
	Free int
}

// ReadHugepagesPool reads a /proc/meminfo-style file and returns the host's
// hugepage pool. The caller passes the path so tests can use a fixture;
// production callers pass /proc/meminfo.
//
// A missing or malformed key is an error naming the file and the key rather
// than a zero value: silently returning zeroes would turn a host
// misconfiguration into a misleading "0 free pages" rejection.
func ReadHugepagesPool(path string) (HugepagesPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return HugepagesPool{}, fmt.Errorf("failed to read hugepage pool from %s: %w", path, err)
	}

	var (
		pool                          HugepagesPool
		haveSize, haveTotal, haveFree bool
	)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		var dst *int
		var seen *bool
		switch strings.TrimSpace(key) {
		case meminfoHugepagesize:
			dst, seen = &pool.PageSizeKB, &haveSize
		case meminfoHugePagesTotal:
			dst, seen = &pool.Total, &haveTotal
		case meminfoHugePagesFree:
			dst, seen = &pool.Free, &haveFree
		default:
			continue
		}

		n, err := parseMeminfoCount(path, strings.TrimSpace(key), value)
		if err != nil {
			return HugepagesPool{}, err
		}
		*dst, *seen = n, true
	}

	if missing := firstMissingHugepagesKey(haveSize, haveTotal, haveFree); missing != "" {
		return HugepagesPool{}, fmt.Errorf("%s: missing %q entry", path, missing)
	}
	return pool, nil
}

// firstMissingHugepagesKey names the first required meminfo key that was not
// seen, in the order the struct documents them.
func firstMissingHugepagesKey(haveSize, haveTotal, haveFree bool) string {
	switch {
	case !haveSize:
		return meminfoHugepagesize
	case !haveTotal:
		return meminfoHugePagesTotal
	case !haveFree:
		return meminfoHugePagesFree
	default:
		return ""
	}
}

// parseMeminfoCount parses the numeric value of one meminfo line. Hugepagesize
// reports a trailing "kB" unit and the page counts do not, so either shape is
// accepted; any other unit is rejected rather than guessed at.
func parseMeminfoCount(path, key, value string) (int, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 2 {
		return 0, fmt.Errorf("%s: malformed %q entry %q", path, key, strings.TrimSpace(value))
	}
	if len(fields) == 2 && !strings.EqualFold(fields[1], "kB") {
		return 0, fmt.Errorf("%s: unsupported unit in %q entry %q: want kB", path, key, strings.TrimSpace(value))
	}

	n, err := strconv.Atoi(fields[0])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: invalid number in %q entry %q", path, key, strings.TrimSpace(value))
	}
	return n, nil
}

// RequiredHugepages is the number of 2M pages needed to back sizeMiB of guest
// memory. Firecracker maps guest memory in 2M pages, so the size must be a
// positive even number of MiB: an odd size would leave a partial page that can
// never be mapped.
func RequiredHugepages(sizeMiB int) (int, error) {
	if sizeMiB <= 0 {
		return 0, fmt.Errorf("invalid hugepage-backed memory size %d MiB: must be positive", sizeMiB)
	}
	if sizeMiB%hugepagesMiB2M != 0 {
		return 0, fmt.Errorf(
			"invalid hugepage-backed memory size %d MiB: 2M huge pages require a multiple of %d MiB",
			sizeMiB, hugepagesMiB2M)
	}
	return sizeMiB / hugepagesMiB2M, nil
}

// HugepagesError reports that a hugepage-backed VM cannot start on this host.
// It always names the request, the pages it needs, the host pool, and the
// action that fixes the host.
type HugepagesError struct {
	// SizeMiB is the guest memory size the VM requested.
	SizeMiB int

	// RequiredPages is the 2M page count that size needs.
	RequiredPages int

	// PageSizeKB is the host's configured hugepage size in KiB.
	PageSizeKB int

	// Free is the host pool's free page count.
	Free int

	// Total is the host pool's allocated page count.
	Total int

	// Reason is the specific failure and, where one exists, the remedy.
	Reason string
}

// Error renders the request, the host pool, and the remedy.
func (e *HugepagesError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cannot back a %d MiB VM with 2M huge pages", e.SizeMiB)
	if e.RequiredPages > 0 {
		fmt.Fprintf(&b, " (%d pages needed)", e.RequiredPages)
	}
	fmt.Fprintf(&b, ": the host pool has %d free of %d allocated", e.Free, e.Total)
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	return b.String()
}

// CheckHugepages is the pre-flight check for a hugepage-backed VM: the host
// must be configured with 2M pages and have enough of them free to back sizeMiB
// of guest memory. Every rejection is a *HugepagesError, so a caller can
// type-assert one type and surface the operator-facing message unchanged.
//
// Checks run page size first, then request validity, then the free-vs-required
// comparison: a wrong-page-size host is the more fundamental misconfiguration,
// so it is reported even when the request itself is also unusable.
func CheckHugepages(pool HugepagesPool, sizeMiB int) error {
	required, reqErr := RequiredHugepages(sizeMiB)

	if pool.PageSizeKB != hugepageSizeKB2M {
		// Only a valid request has a meaningful page count to report.
		requiredPages := 0
		if reqErr == nil {
			requiredPages = required
		}
		return &HugepagesError{
			SizeMiB:       sizeMiB,
			RequiredPages: requiredPages,
			PageSizeKB:    pool.PageSizeKB,
			Free:          pool.Free,
			Total:         pool.Total,
			Reason: fmt.Sprintf(
				"the host's hugepage size is %d KiB, but Firecracker supports only 2M (%d KiB) pages: reconfigure the host pool with 2M pages",
				pool.PageSizeKB, hugepageSizeKB2M),
		}
	}

	if reqErr != nil {
		return &HugepagesError{
			SizeMiB:    sizeMiB,
			PageSizeKB: pool.PageSizeKB,
			Free:       pool.Free,
			Total:      pool.Total,
			Reason:     reqErr.Error(),
		}
	}

	if pool.Free < required {
		// vm.nr_hugepages sets the pool total, not an increment, so the
		// operator must size it to the pages already in use plus this VM's
		// pages; setting it to required alone would still leave the VM short.
		target := hugepagesPoolTarget(pool, required)
		return &HugepagesError{
			SizeMiB:       sizeMiB,
			RequiredPages: required,
			PageSizeKB:    pool.PageSizeKB,
			Free:          pool.Free,
			Total:         pool.Total,
			Reason: fmt.Sprintf(
				"raise the pool total to %d pages with sysctl vm.nr_hugepages=%d",
				target, target),
		}
	}

	return nil
}

// hugepagesPoolTarget is the vm.nr_hugepages value that would leave required
// pages free: the pages already in use plus this VM's pages. A Free greater
// than Total cannot happen on a real host, but clamping the in-use count at
// zero keeps a malformed fixture from suggesting a total below required.
func hugepagesPoolTarget(pool HugepagesPool, required int) int {
	inUse := pool.Total - pool.Free
	if inUse < 0 {
		inUse = 0
	}
	return inUse + required
}
