//go:build linux

package agent

import "syscall"

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

// charsToString converts a fixed-size uname field to a string, stopping at the
// NUL the kernel pads with. The field holds signed chars on every Linux
// architecture Go supports, so each one is widened before it is treated as a
// byte.
func charsToString(field []int8) string {
	buf := make([]byte, 0, len(field))
	for _, c := range field {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf)
}
