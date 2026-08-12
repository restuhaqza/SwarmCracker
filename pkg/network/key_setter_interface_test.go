package network

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// Compile-time proof: *NetworkManager satisfies the swarmkit NetworkKeySetter
// interface signature. If this signature ever drifts again, this test fails to compile.
func TestNetworkManager_SatisfiesKeySetterSignature(t *testing.T) {
	var nm = NewNetworkManager(types.NetworkConfig{})
	_, ok := nm.(interface {
		SetEncryptionKeys(keys []*api.EncryptionKey) error
	})
	if !ok {
		t.Fatal("NetworkManager no longer satisfies NetworkKeySetter (SetEncryptionKeys([]*api.EncryptionKey) error)")
	}
}
