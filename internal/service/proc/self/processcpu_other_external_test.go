//go:build !unix && !windows

package self_test

import "time"

// processCPU answers nothing where the standard library reaches no kernel
// count of the process's CPU time.
func processCPU() (time.Duration, bool) {
	return 0, false
}
