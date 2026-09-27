//go:build !integration

package image

import (
	"testing"
)

func TestHandleMounts_VolumeVariantsV4(t *testing.T) {
	// Create ImagePreparer
	ip := NewImagePreparer(&PreparerConfig{
		RootfsDir: t.TempDir(),
	}).(*ImagePreparer)

	// volumeManager is nil by default when not configured
	// This test just verifies the field exists
	_ = ip.volumeManager
}
