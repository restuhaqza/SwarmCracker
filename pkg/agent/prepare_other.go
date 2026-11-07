//go:build !linux

package agent

import (
	"context"
	"fmt"
	"runtime"
)

// The guest agent is a Linux binary; the package still compiles elsewhere so the
// host-side client can be built and tested on a development machine. There is no
// guest to reconfigure on a non-Linux host, so `prepare` fails loudly rather than
// reporting an identity it never applied.

// reachMetadataServiceOS is unsupported off Linux.
func reachMetadataServiceOS(string) error {
	return fmt.Errorf("prepare: reaching the metadata service is unsupported on %s", runtime.GOOS)
}

// applyIdentityOS is unsupported off Linux.
func applyIdentityOS(context.Context, Identity) (PrepareReport, error) {
	return PrepareReport{}, fmt.Errorf("prepare: applying an identity is unsupported on %s", runtime.GOOS)
}
