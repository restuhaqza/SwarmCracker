//go:build linux && !integration

package network

// The bridge a task's TAP joins is derived once, in this package, and both paths that
// attach a TAP read that answer: the boot path (createTapDevice) and a task restored from
// a migration artifact, which never runs the code that prepares a task's networks. A
// second copy of the rule is how a migrated task on a user-defined network would come up
// on the default bridge, with an address that belongs to another L2 segment.
//
// managed_test.go pins the derivation itself; this file pins that the exported form is the
// same function, so the restore path cannot drift from the boot path.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestExportedAttachmentBridgeNameIsTheBootPathsAnswer pins the export: every case the
// manager answers is answered identically by the package-level function the restore path
// calls.
func TestExportedAttachmentBridgeNameIsTheBootPathsAnswer(t *testing.T) {
	nm := &NetworkManager{config: types.NetworkConfig{BridgeName: "swarm-br0", Subnet: "192.168.127.0/24"}}

	networks := []*types.Network{
		nil,
		{Spec: types.NetworkSpec{Name: "ingress", Subnet: "192.168.127.0/24",
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-ingress"}}}},
		{Spec: types.NetworkSpec{Subnet: "10.10.0.0/24",
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-backend"}}}},
		{ID: "abcdefgh12345678", Spec: types.NetworkSpec{Driver: "overlay", Subnet: "10.20.0.0/24"}},
		{ID: "net-1", Spec: types.NetworkSpec{Driver: "bridge", Subnet: "10.30.0.0/24"}},
	}

	for _, network := range networks {
		assert.Equal(t, nm.attachmentBridgeName(network), AttachmentBridgeName(network, nm.config),
			"the restore path and the boot path derive one answer, not two")
	}

	assert.Equal(t, "br-backend", AttachmentBridgeName(networks[2], nm.config))
	assert.Equal(t, "swarm-br0", AttachmentBridgeName(networks[0], nm.config),
		"a task with no attachment joins the node's bridge")
}
