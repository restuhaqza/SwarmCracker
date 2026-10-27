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

// TestGenerateWrapperScript_ArgvOverride verifies the generated init wrapper
// runs a service task's --command/--args instead of the image's OCI command,
// and keeps the image command when the task overrides nothing.
func TestGenerateWrapperScript_ArgvOverride(t *testing.T) {
	script := generateWrapperScript(&OCIImageInfo{ImageRef: "nginx"}, 10)

	assert.Contains(t, script, "sc.argv=", "the wrapper must read the per-task argv")
	assert.Contains(t, script, "base64 -d", "argv elements are base64 encoded on the command line")
	assert.Contains(t, script, `set -- "$@" "${_sc_arg#x}"`,
		"each decoded element must become one positional parameter")
	assert.Contains(t, script, `-- "$@"`,
		"the override must be exec'd with the decoded argv")
	assert.Contains(t, script, "-- /bin/sh",
		"the image command must remain the fallback when no sc.argv is present")

	// A missing base64 applet must fail loudly rather than silently running the
	// image command the operator asked to replace.
	assert.Contains(t, script, "command -v base64")
	assert.Contains(t, script, "exit 127")
}

func TestGuestOverrideMarker(t *testing.T) {
	dir := t.TempDir()
	rootfs := dir + "/image.ext4"

	ip := &ImagePreparer{}
	assert.False(t, ip.guestOverrideReady(rootfs), "no marker yet")

	ip.writeGuestOverrideMarker(rootfs)
	assert.True(t, ip.guestOverrideReady(rootfs), "marker written")
}
