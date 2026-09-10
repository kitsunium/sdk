// Package health — hosts the opt-in sd_notify wiring. It WIRES the notifier
// the SDK already ships; it does not reimplement one.
package health

import (
	"errors"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// readyState is the sd_notify state name used in a NotifyFailed field. It is
// the protocol's own spelling, so an operator reading the error can grep the
// unit's journal for the same word.
const readyState string = "READY"

// statusState is its counterpart for the STATUS= line.
const statusState string = "STATUS"

// announce delivers the opt-in sd_notify state for one readiness aggregate:
// READY=1 the first time the replica can serve, and STATUS= on every change
// after that.
//
// It is driven by READINESS on purpose. "Every component constructed" and
// "this replica can answer a request" are two different claims, and a
// supervisor acting on the first while the second is false routes traffic to a
// process that cannot serve it. lifecycle.RunConfig.Notify makes the earlier
// claim; a caller picks one of the two, not both.
func (h *health) announce(status corehealth.Status) {
	//: the SDK sends nothing it was not asked to send — a datagram is a
	//: process-wide, observable side effect.
	if !h.cfg.Notify {
		//: silent by default.
		return
	}
	state, changed := h.notifyState(status)
	//: nothing new to tell the supervisor.
	if !changed {
		//: one datagram per change, never one per poll.
		return
	}
	//: READY is the transition that matters; everything after it is status.
	if state == readyState {
		//: deliver once, and only once ever.
		h.deliver(sdnotify.Ready, readyState)
		//: nothing else is owed on the transition that matters.
		return
	}
	//: a serving/not-serving change after readiness, as a human-readable
	//: line. It names the aggregate and nothing else: a STATUS line is shown
	//: in `systemctl status`, which is at least as public as a probe body.
	h.deliver(func() error { return sdnotify.Status("health: " + status.String()) }, state)
}

// notifyState decides what should be sent for this aggregate, and records the
// decision so the same thing is not sent twice.
func (h *health) notifyState(status corehealth.Status) (state string, changed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	//: the first serving aggregate is the readiness the supervisor waits for.
	//: A not-serving aggregate before it says nothing at all: a unit that has
	//: never been ready is exactly what systemd already assumes.
	if !h.notified {
		//: still not serving — stay silent rather than announce a state the
		//: supervisor would read as a regression from a claim never made.
		if !status.Serving() {
			//: nothing to send.
			return "", false
		}
		h.notified = true
		h.lastNotified = status
		//: the one READY=1 of this process's life.
		return readyState, true
	}
	//: after READY, only a change is worth a datagram.
	if status == h.lastNotified {
		//: nothing new.
		return "", false
	}
	h.lastNotified = status
	//: a status line naming the new aggregate.
	return statusState, true
}

// deliver sends one datagram and routes a failure to the caller's hook.
func (h *health) deliver(send func() error, state string) {
	err := send()
	//: two ways there is nothing left to do. Either the datagram went out —
	//: which includes the documented no-op that returns nil when
	//: $NOTIFY_SOCKET is unset, so an unsupervised binary stays silent rather
	//: than failing — or it did not and there is no hook to tell: the SDK
	//: writes nothing to stderr on the caller's behalf, and never to stdout at
	//: all (ADR 0030).
	if err == nil || h.cfg.OnNotifyError == nil {
		//: delivered, deliberately silent, or dropped for want of a listener.
		return
	}
	//: side by side rather than wrapped: origin-wins would relabel
	//: NOTIFY_FAILED with whatever sdnotify reported, and the hook is the
	//: caller's own log, where both halves belong.
	h.cfg.OnNotifyError(errors.Join(kerrs.Wrap(NotifyFailed, kerrs.WrapParams{},
		kerrs.String("state", state)), err))
}
