package translator

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestGuestOverrideBootArgs(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		assert.Empty(t, guestOverrideBootArgs(&types.Container{}))
	})

	t.Run("nil container", func(t *testing.T) {
		assert.Empty(t, guestOverrideBootArgs(nil))
	})

	t.Run("hostname and dns", func(t *testing.T) {
		got := guestOverrideBootArgs(&types.Container{
			Hostname: "node-a",
			DNS:      []string{"1.1.1.1", "8.8.8.8"},
		})
		assert.Equal(t, []string{"sc.hostname=node-a", "sc.dns=1.1.1.1,8.8.8.8"}, got)
	})
}

func TestBuildBootArgs_IncludesGuestOverrides(t *testing.T) {
	tt := &TaskTranslator{}
	task := &types.Task{
		ID: "t1",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Command:  []string{"/bin/sh"},
				Hostname: "web-1",
				DNS:      []string{"9.9.9.9"},
			},
		},
	}

	got := tt.buildBootArgs(task)
	assert.Contains(t, got, "sc.hostname=web-1")
	assert.Contains(t, got, "sc.dns=9.9.9.9")
}

func TestBuildBootArgs_NoOverridesWhenUnset(t *testing.T) {
	tt := &TaskTranslator{}
	task := &types.Task{
		ID: "t2",
		Spec: types.TaskSpec{
			Runtime: &types.Container{Command: []string{"/bin/sh"}},
		},
	}

	got := tt.buildBootArgs(task)
	assert.NotContains(t, got, "sc.hostname=")
	assert.NotContains(t, got, "sc.dns=")
}
