//go:build linux

package network

// NewVXLANManagerWithExecutor creates a new VXLAN manager with a custom
// netlink executor. This is a test-only seam used to inject mock
// implementations; production code uses NewVXLANManager.
func NewVXLANManagerWithExecutor(bridgeName string, vxlanID int, overlayIP string, peerStore PeerStore, executor NetlinkExecutor) *VXLANManager {
	if peerStore == nil {
		peerStore = NewStaticPeerStore(nil)
	}
	if executor == nil {
		executor = NewDefaultNetlinkExecutor()
	}
	return &VXLANManager{
		BridgeName:      bridgeName,
		VXLANID:         vxlanID,
		OverlayIP:       overlayIP,
		vxlanPort:       4789,
		peerStore:       peerStore,
		netlinkExecutor: executor,
	}
}
