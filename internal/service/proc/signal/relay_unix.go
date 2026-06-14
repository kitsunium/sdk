//go:build unix

// Package signal — Unix Relay: forwards each received signal to a pid or process
// group via kill(2), where a negative Target addresses the group whose id is its
// absolute value.
package signal

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR — the exit status RelayFailed carries so a
// supervisor surfacing err.ExitCode() reports an OS-level fault, not a generic 70.
const exitOSErr int = 71

// Relay reads signals from src and forwards each to target via kill(2) until src
// is closed. A positive target is a pid; a target below -1 addresses the process
// group whose id is -target (the kill(2) negative-pid convention). The first
// delivery failure stops the relay and returns a RelayFailed error wrapping the
// kill(2) cause; a clean drain of src returns nil.
func Relay(src <-chan coreproc.Signal, target Target) error {
	//: forward every signal the source emits until it is closed by the owner.
	for sig := range src {
		//: a non-syscall carrier cannot be delivered by kill(2); skip it rather
		//: than abort the relay on an impossible value.
		osSig, ok := sig.OS().(syscall.Signal)
		//: only a real syscall.Signal can be handed to kill(2).
		if !ok {
			//: drop the un-deliverable value and keep relaying the rest.
			continue
		}
		//: deliver to the pid or, for a negative target, to the process group.
		if kerr := syscall.Kill(int(target), osSig); kerr != nil {
			//: a failed delivery is fatal to the relay — surface it typed.
			return errs.Wrap(kerr, errs.WrapParams{
				Code:     coreproc.CodeRelayFailed,
				Reason:   "RELAY_FAILED",
				Public:   "Could not relay the signal to the target",
				Private:  "service/proc/signal.Relay: forwarding the signal to the target failed",
				ExitCode: exitOSErr,
			}, errs.Int("target", int(target)), errs.Int("signal", sig.Int()))
		}
	}
	//: the source drained cleanly with no delivery error.
	return nil
}
