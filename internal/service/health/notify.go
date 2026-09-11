// Package health — hosts the opt-in sd_notify wiring. It WIRES the notifier
// the SDK already ships; it does not reimplement one.
package health

import (
	"context"
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
//
// # What has been announced is what was DELIVERED
//
// The announced state moves only once a datagram has actually gone out. It
// used to move before the send: a READY=1 that failed was then recorded as
// sent and never sent again, and a Type=notify unit is killed at
// TimeoutStartSec by a supervisor still waiting for it — while every probe
// reported the replica ready. The same ordering left a STATUS= line that failed
// recorded as the supervisor's current text, stale until the aggregate happened
// to change again.
func (h *health) announce(ctx context.Context, status corehealth.Status) {
	//: the SDK sends nothing it was not asked to send — a datagram is a
	//: process-wide, observable side effect.
	if !h.cfg.Notify {
		//: silent by default.
		return
	}
	state, err := h.notify(ctx, status)
	//: two ways there is nothing left to do. Either nothing was owed or the
	//: datagram went out — which includes the documented no-op that returns
	//: nil when $NOTIFY_SOCKET is unset, so an unsupervised binary stays silent
	//: rather than failing — or it failed and there is no hook to tell: the SDK
	//: writes nothing to stderr on the caller's behalf, and never to stdout at
	//: all (ADR 0030).
	if err == nil || h.cfg.OnNotifyError == nil {
		//: delivered, deliberately silent, or dropped for want of a listener.
		return
	}
	//: side by side rather than wrapped: origin-wins would relabel
	//: NOTIFY_FAILED with whatever sdnotify reported, and the hook is the
	//: caller's own log, where both halves belong. It is called after the lock
	//: is released, so a hook that probes again cannot deadlock on it.
	h.cfg.OnNotifyError(errors.Join(kerrs.Wrap(NotifyFailed, kerrs.WrapParams{},
		kerrs.String("state", state)), err))
}

// notify decides what this aggregate owes the supervisor, sends it, and only
// then records it as announced.
//
// The decision, the send and the commit happen under one lock, notifyMu. Two
// probes that decided in one order and sent in the other would leave the
// supervisor showing the OLDER status line, recorded here as the newer one —
// with nothing left that would ever correct it.
//
// The phase is read under the same lock. A readiness probe measures its
// dependencies BEFORE it announces, so one that began while the process was
// serving can finish after Drain, and after another probe has already
// announced the drain; its serving aggregate would then be sent last and leave
// the supervisor showing healthy for the rest of the drain. Draining is
// one-way, so once it has begun a serving status is owed to nobody, and the
// decision reads the phase where it can no longer change under it.
//
// The send is BOUNDED, which is what makes serialising it affordable. A
// unixgram write blocks once the supervisor's receive buffer is full, and
// nothing in this process can make it drain; an unbounded send under this lock
// meant one stuck supervisor stopped every later readiness probe from
// answering at all — the readiness the datagram announces, made unobservable by
// the act of announcing it. The bound is the PROBE's deadline where the caller
// set one, so the announcement can never outlive the answer it belongs to, and
// otherwise the same budget one check gets (Config.DefaultTimeout, else
// DefaultCheckTimeout): a datagram nobody is reading is exactly as unavailable
// as a dependency nobody is answering, and the registry already has a number
// for that.
func (h *health) notify(ctx context.Context, status corehealth.Status) (state string, err error) {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()
	//: a verdict measured before the drain, arriving after it.
	if status.Serving() && h.phase() == phaseDraining {
		//: nothing owed: the drain's own announcement stands.
		return "", nil
	}
	state, send := h.owed(status)
	//: nothing new to tell the supervisor — one datagram per change, never
	//: one per poll.
	if send == nil {
		//: nothing sent, nothing to commit.
		return "", nil
	}
	//: bound the datagram before sending it, so the lock is held for a stated
	//: time rather than for as long as the supervisor takes.
	sendCtx, release := h.sendBudget(ctx)
	defer release()
	//: a failed send commits nothing, so the next probe decides again — and
	//: owes the supervisor the same datagram. A send that ran out of budget is
	//: one of those failures: the datagram did not go out.
	if serr := send(sendCtx); serr != nil {
		//: the caller routes it to the hook.
		return state, serr
	}
	h.notified = true
	h.lastNotified = status
	//: delivered, and now announced.
	return state, nil
}

// sendBudget derives the context one datagram is allowed to take, and returns
// the release that must run on every path out.
//
// The probe's own deadline wins whenever it has one: the announcement belongs
// to that answer and must not outlive it, and a deadline the caller set needs
// no machinery at all — sdnotify hands it to the kernel as the socket's write
// deadline. Without one it takes the budget a check would get, because an
// unreadable notify socket and an unresponsive dependency are the same kind of
// wait and the registry already states how long it tolerates one.
//
// # Goroutine lifetime
//
// The second branch starts ONE goroutine per datagram, and it is armed on the
// INJECTED clock rather than through context.WithTimeout — this package waits
// on no wall clock, which its own AST audit enforces, and a budget that cannot
// be moved by a test is a budget nothing verifies. It ends on whichever comes
// first: the timer, or the returned release, which every caller defers. It
// therefore cannot outlive the send.
func (h *health) sendBudget(ctx context.Context) (bounded context.Context, release func()) {
	//: a caller who set a deadline has already stated the bound.
	if _, ok := ctx.Deadline(); ok {
		//: nothing armed, nothing to release.
		return ctx, func() {}
	}
	//: otherwise the registry's own per-check budget, never zero.
	bounded, cancel := context.WithCancel(ctx)
	timer := h.clk.NewTimer(h.cfg.checkBudget(0))
	go func() {
		defer timer.Stop()
		select {
		//: the supervisor had its budget and did not read.
		case <-timer.C():
			//: unblocks the write, which then reports what went wrong.
			cancel()
		//: the send finished, or the caller left first.
		case <-bounded.Done():
			//: nothing to do; the deferred Stop releases the timer.
		}
	}()
	//: the cancel releases both the context and the watcher above.
	return bounded, cancel
}

// owed decides what, if anything, this aggregate owes the supervisor. It reads
// the announced state and changes nothing: only a delivered datagram does.
func (h *health) owed(status corehealth.Status) (state string, send func(ctx context.Context) error) {
	//: the first serving aggregate is the readiness the supervisor waits for.
	//: A not-serving aggregate before it says nothing at all: a unit that has
	//: never been ready is exactly what systemd already assumes.
	if !h.notified {
		//: still not serving — stay silent rather than announce a state the
		//: supervisor would read as a regression from a claim never made.
		if !status.Serving() {
			//: nothing owed.
			return "", nil
		}
		//: READY=1, owed until it is delivered once.
		return readyState, sdnotify.ReadyContext
	}
	//: after READY, only a change is worth a datagram.
	if status == h.lastNotified {
		//: nothing new.
		return "", nil
	}
	//: a serving/not-serving change after readiness, as a human-readable line.
	//: It names the aggregate and nothing else: a STATUS line is shown in
	//: `systemctl status`, which is at least as public as a probe body.
	return statusState, func(ctx context.Context) error {
		//: the aggregate's own words, bounded by the probe that measured it.
		return sdnotify.StatusContext(ctx, "health: "+status.String())
	}
}
