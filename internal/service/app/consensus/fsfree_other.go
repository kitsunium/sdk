//go:build !(linux || darwin)

package consensus

// fsFree is unknown on this platform.
func fsFree(string) int64 { return -1 }
