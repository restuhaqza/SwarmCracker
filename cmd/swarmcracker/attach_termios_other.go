//go:build !linux

package main

// makeRaw is a no-op on platforms where SwarmCracker's Firecracker-backed
// commands are not supported.
func makeRaw(_ int) (restore func(), ok bool) {
	return func() {}, false
}
