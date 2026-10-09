package image

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGenerateWrapperScript_GuestOverrides verifies the generated init wrapper
// applies the per-task hostname/DNS overrides passed on the kernel command line.
func TestGenerateWrapperScript_GuestOverrides(t *testing.T) {
	script := generateWrapperScript(&OCIImageInfo{ImageRef: "nginx"}, 10)

	assert.Contains(t, script, "sc.hostname=")
	assert.Contains(t, script, "sc.dns=")
	assert.Contains(t, script, `hostname "$_SC_HOST"`)
	assert.Contains(t, script, "nameserver $_ns")
	assert.Contains(t, script, "/etc/resolv.conf")
	assert.Contains(t, script, "/etc/hostname")
}

func TestGuestOverrideMarker(t *testing.T) {
	dir := t.TempDir()
	rootfs := dir + "/image.ext4"

	ip := &ImagePreparer{}
	assert.False(t, ip.guestOverrideReady(rootfs), "no marker yet")

	ip.writeGuestOverrideMarker(rootfs)
	assert.True(t, ip.guestOverrideReady(rootfs), "marker written")
}
