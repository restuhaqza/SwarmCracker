//go:build !unix

package agent

// exitSignal reports no terminating signal on platforms without wait statuses.
// SwarmCracker targets Linux; this keeps the package buildable elsewhere.
func exitSignal(error) int {
	return 0
}
