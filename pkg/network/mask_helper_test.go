package network

import "net"

// testMaskToPrefix is a test-only replacement for the removed maskToPrefix
// production helper.
func testMaskToPrefix(mask net.IPMask) int {
	ones, _ := mask.Size()
	return ones
}
