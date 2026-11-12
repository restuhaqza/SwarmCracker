//go:build linux

package agent

import (
	"strings"
	"syscall"
)

// machineArch names the architecture the way the guest's own kernel does, which
// is what a template manifest and the guest's `uname -m` say. runtime.GOARCH
// would name the same architecture "amd64" and "arm64", which is not the string
// anything else in the template ecosystem uses.
func machineArch() string {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	return charsToString(uts.Machine[:])
}

// charsToString converts a fixed-size uname field to a string.
//
// The field holds signed chars on every Linux architecture Go supports, and the
// kernel pads it with NULs. Each one is widened to a rune and never narrowed to a
// byte, so no value can be truncated. A byte outside ASCII cannot be part of a
// machine name, so a field that holds one is reported as unknown rather than as a
// shortened name: a mangled "x86_64" would read as an architecture this host has,
// which is worse than reading as none.
func charsToString(field []int8) string {
	var name strings.Builder
	name.Grow(len(field))
	for _, c := range field {
		if c == 0 {
			break
		}
		if c < 0 {
			return ""
		}
		name.WriteRune(rune(c))
	}
	return name.String()
}
