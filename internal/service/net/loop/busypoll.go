package loop

import (
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// busy-poll's window and budget (#979, ADR 0189 amended 2026-11-11).
const (
	// defaultBusyPoll is the window busy-poll takes when LoopValue.BusyPoll is
	// zero: the value Documentation/networking/napi.rst gives its examples,
	// held until the paired gate measures another.
	defaultBusyPoll time.Duration = 50 * time.Microsecond
	// maxBusyPoll bounds LoopValue.BusyPoll: past a second a loop would spin
	// rather than wait.
	maxBusyPoll time.Duration = time.Second
	// maxBusyPollBudget bounds LoopValue.BusyPollBudget: epoll_params'
	// busy_poll_budget is 16 bits.
	maxBusyPollBudget int = 1<<16 - 1
	// reasonBusyPollOn is the row's reason once on: what it polls, and what it
	// cannot.
	reasonBusyPollOn string = "polls the NAPI ids of the loop's sockets; loopback and a veth without GRO have none"
)

// busyPollPlan is what busy-poll asks of the kernel: the window in whole
// microseconds and epoll's budget, zero for the kernel's.
type busyPollPlan struct {
	usecs  uint32
	budget uint16
}

// busyPollOf resolves v's window, rounded up to whole microseconds, and its
// budget; checkValue already held both to their ranges.
func busyPollOf(v corenet.LoopValue) busyPollPlan {
	d := time.Duration(v.BusyPoll)
	//: the default window.
	if d <= 0 {
		d = defaultBusyPoll
	}
	d = min(d, maxBusyPoll)
	usecs := (d + time.Microsecond - 1) / time.Microsecond
	//: in the kernel's units.
	return busyPollPlan{usecs: uint32(usecs), budget: uint16(min(max(v.BusyPollBudget, 0), maxBusyPollBudget))}
}

// busyPollField names the busy-poll field of v out of its range, "" when
// both are in it.
func busyPollField(v corenet.LoopValue) string {
	//: by the first out of range.
	switch {
	//: no negative window, none past a second.
	case v.BusyPoll < 0 || time.Duration(v.BusyPoll) > maxBusyPoll:
		//: named.
		return "BusyPoll"
	//: what 16 bits hold.
	case v.BusyPollBudget < 0 || v.BusyPollBudget > maxBusyPollBudget:
		//: named.
		return "BusyPollBudget"
	}
	//: in range.
	return ""
}

// callHook tells a test's counter about a busy-poll call about to be issued.
func callHook(hook func(string), call string) {
	//: production has none.
	if hook != nil {
		hook(call)
	}
}
