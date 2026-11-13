//go:build !linux

package agent

import "runtime"

// machineArch is the non-Linux build's stand-in for the uname field: this package
// only ever runs as the guest agent on Linux, so anywhere else the Go
// architecture is the closest true answer, and it exists so the tree keeps
// building on a development host.
func machineArch() string { return runtime.GOARCH }
