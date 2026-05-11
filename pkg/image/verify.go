package image

import (
	"fmt"
	"os/exec"

	"github.com/rs/zerolog/log"
)

// VerifyBootable checks if a rootfs image has all required files for booting.
// Uses debugfs to inspect the ext4 image without mounting.
func VerifyBootable(rootfsPath string) error {
	// Check debugfs availability
	if _, err := exec.LookPath("debugfs"); err != nil {
		log.Debug().Msg("debugfs not available, skipping bootable verification")
		//nolint:nilerr
		return nil
	}

	requiredPaths := []string{
		"/init",
		"/sbin/init",
		"/sbin/tini",
		"/bin/sh",
		"/etc/resolv.conf",
	}

	var missing []string
	for _, path := range requiredPaths {
		cmd := exec.Command("debugfs", "-R", "stat "+path, rootfsPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			missing = append(missing, path)
			log.Debug().Str("path", path).Err(err).Str("output", string(output)).Msg("Missing required file")
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("rootfs verification failed — missing: %v", missing)
	}

	log.Info().Str("path", rootfsPath).Msg("Rootfs verified bootable")
	return nil
}
