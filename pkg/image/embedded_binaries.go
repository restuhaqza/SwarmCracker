// Package image provides init system injection for container images.
package image

import _ "embed"

// Embedded static binaries for injection into VM rootfs.
// These are compiled for amd64 and provide init/shell capabilities.

//go:embed binaries/tini-amd64
var tiniBinary []byte

//go:embed binaries/busybox-amd64
var busyboxBinary []byte
