package image

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseDiskSize parses a human-readable size into bytes. Accepted units use
// binary multiples: "10G", "10GB", "10GiB", "512m", "1024K", or a plain byte
// count ("1073741824"). An empty string yields 0 (meaning "no minimum").
func ParseDiskSize(size string) (int64, error) { return parseDiskSize(size) }

// parseDiskSize parses a human-readable size into bytes. Accepted units use
// binary multiples: "10G", "10GB", "10GiB", "512m", "1024K", or a plain byte
// count ("1073741824"). An empty string yields 0 (meaning "no minimum").
func parseDiskSize(size string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(size))
	if s == "" {
		return 0, nil
	}

	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("missing numeric value in %q", size)
	}

	value, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", size, err)
	}

	var multiplier float64
	switch strings.TrimSpace(s[i:]) {
	case "", "B":
		multiplier = 1
	case "K", "KB", "KIB":
		multiplier = 1 << 10
	case "M", "MB", "MIB":
		multiplier = 1 << 20
	case "G", "GB", "GIB":
		multiplier = 1 << 30
	case "T", "TB", "TIB":
		multiplier = 1 << 40
	default:
		return 0, fmt.Errorf("unknown unit %q in %q", strings.TrimSpace(s[i:]), size)
	}

	if value < 0 {
		return 0, fmt.Errorf("size must not be negative: %q", size)
	}

	return int64(value * multiplier), nil
}

// firstInt64 returns the first value, or 0 when the slice is empty.
func firstInt64(values []int64) int64 {
	if len(values) > 0 {
		return values[0]
	}
	return 0
}

// rootfsLargeEnough reports whether a rootfs of the given size satisfies an
// optional requested minimum (0 means no minimum).
func rootfsLargeEnough(size, requested int64) bool {
	return requested <= 0 || size >= requested
}
