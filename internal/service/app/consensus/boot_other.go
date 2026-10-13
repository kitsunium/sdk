//go:build !linux && !darwin

package consensus

// bootID is unknown on this platform: a STORAGE_FAILED marker then refuses
// every restart until an operator clears it, the safe side.
func bootID() string { return "" }
