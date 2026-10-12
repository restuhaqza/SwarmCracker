//go:build !linux

package main

import (
	"errors"
	"net"
)

// errVsockUnsupported is returned by the non-linux stub. AF_VSOCK is a
// Linux-only address family; the stub keeps the tree buildable on development
// hosts such as darwin. The runtime binary is only ever built for linux.
var errVsockUnsupported = errors.New("vsock is only supported on linux")

// Listen is the non-linux stub.
func Listen(_ uint32) (net.Listener, error) {
	return nil, errVsockUnsupported
}
