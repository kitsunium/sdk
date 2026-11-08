//go:build windows

package service

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// platformSignals returns no SIGCONT and no SIGHUP: Windows delivers a kill
// and nothing else, so the engine sends neither.
func platformSignals() (sigcont, sighup coreproc.Signal) {
	//: neither is sent.
	return 0, 0
}
