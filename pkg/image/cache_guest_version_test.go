package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGuestOverrideReady_MarkerStates verifies that a cached rootfs is only
// considered current when its .guest-version marker matches the running guest
// image. A missing or older marker must be reported as stale so the cache is
// rebuilt (see TestPrepare_CachedRootfsGuestVersionRebuild).
func TestGuestOverrideReady_MarkerStates(t *testing.T) {
	ip := &ImagePreparer{}

	cases := []struct {
		name   string
		marker string // empty means "no marker file"
		want   bool
	}{
		{name: "missing", marker: "", want: false},
		{name: "older", marker: "2\n", want: false},
		{name: "newer", marker: "4\n", want: false},
		{name: "matching", marker: guestOverrideVersion + "\n", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rootfs := filepath.Join(dir, "image.ext4")
			require.NoError(t, os.WriteFile(rootfs, []byte("rootfs"), 0644))
			if tc.marker != "" {
				require.NoError(t, os.WriteFile(rootfs+".guest-version", []byte(tc.marker), 0644))
			}

			assert.Equal(t, tc.want, ip.guestOverrideReady(rootfs))
		})
	}
}

// markCachedRootfsCurrent writes the current guest-version marker beside a
// cached-rootfs fixture. Prepare requires the marker unconditionally, so a test
// that fabricates a reusable cache must also declare it current.
func markCachedRootfsCurrent(t *testing.T, rootfsPath string) {
	t.Helper()
	require.NoError(t, os.WriteFile(rootfsPath+".guest-version", []byte(guestOverrideVersion+"\n"), 0644))
}

// makeCachedRootfs writes a rootfs at path that verifyCachedRootfs accepts.
// When debugfs is available it builds a real ext4 image containing /init;
// otherwise (the golang test image) any existing file is treated as valid.
func makeCachedRootfs(t *testing.T, path string) {
	t.Helper()

	if _, err := exec.LookPath("debugfs"); err != nil {
		require.NoError(t, os.WriteFile(path, []byte("fake rootfs"), 0644))
		return
	}

	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("debugfs is available but mkfs.ext4 is not; cannot build a valid cached rootfs")
	}

	content := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(content, "init"), []byte("#!/bin/sh\n"), 0755))
	out, err := exec.Command("mkfs.ext4", "-d", content, "-L", "rootfs", path, "25600").CombinedOutput()
	require.NoError(t, err, "mkfs.ext4: %s", string(out))
}

// TestPrepare_CachedRootfsGuestVersionRebuild verifies the cache-invalidation
// fix: a cached rootfs is reused only when its .guest-version marker matches
// the current guest image. A plain service (no --hostname/--dns) whose cache
// carries no marker, or a marker from an older guest image, must be rebuilt
// rather than silently reused.
func TestPrepare_CachedRootfsGuestVersionRebuild(t *testing.T) {
	// A reference that fails parsing immediately means the rebuild path errors
	// out without touching the network or a registry.
	const invalidImage = "not a valid image ref"

	newPreparer := func(rootfsDir string) *ImagePreparer {
		return NewImagePreparer(&PreparerConfig{
			RootfsDir:  rootfsDir,
			InitSystem: "none",
		}).(*ImagePreparer)
	}
	newTask := func() *types.Task {
		return &types.Task{
			ID:          "task-cache-invalidation",
			Annotations: make(map[string]string),
			Spec: types.TaskSpec{
				Runtime: &types.Container{Image: invalidImage},
			},
		}
	}
	cachedPath := func(rootfsDir string) string {
		return filepath.Join(rootfsDir, generateImageID(invalidImage)+".ext4")
	}

	t.Run("matching marker reuses cache", func(t *testing.T) {
		rootfsDir := t.TempDir()
		rootfs := cachedPath(rootfsDir)
		makeCachedRootfs(t, rootfs)

		ip := newPreparer(rootfsDir)
		ip.writeGuestOverrideMarker(rootfs)

		task := newTask()
		require.NoError(t, ip.Prepare(context.Background(), task))
		assert.Equal(t, rootfs, task.Annotations["rootfs"])

		_, err := os.Stat(rootfs)
		require.NoError(t, err, "cached rootfs must be kept when the marker matches")
	})

	for _, tc := range []struct {
		name   string
		marker string
	}{
		{name: "missing marker", marker: ""},
		{name: "older marker", marker: "2\n"},
	} {
		t.Run(tc.name+" rebuilds cache", func(t *testing.T) {
			rootfsDir := t.TempDir()
			rootfs := cachedPath(rootfsDir)
			makeCachedRootfs(t, rootfs)
			if tc.marker != "" {
				require.NoError(t, os.WriteFile(rootfs+".guest-version", []byte(tc.marker), 0644))
			}

			ip := newPreparer(rootfsDir)

			err := ip.Prepare(context.Background(), newTask())
			// The cache must not be reused: preparation falls through to a
			// rebuild and then fails on the deliberately invalid image ref.
			require.Error(t, err)
			assert.Contains(t, err.Error(), "failed to prepare image")

			_, statErr := os.Stat(rootfs)
			assert.True(t, os.IsNotExist(statErr), "stale rootfs must be dropped before rebuilding")
		})
	}
}
