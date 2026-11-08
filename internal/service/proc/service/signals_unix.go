//go:build !windows

package service

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformSignals returns SIGCONT, sent after the kill signal so a stopped
// process can end, and SIGHUP, sent after it on SendSIGHUP=.
func platformSignals() (sigcont, sighup coreproc.Signal) {
	//: both exist off Windows.
	return coreproc.Signal(syscall.SIGCONT), coreproc.Signal(syscall.SIGHUP)
}
