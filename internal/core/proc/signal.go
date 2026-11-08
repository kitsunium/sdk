package proc

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// The realtime range's two names, as kill(1) and systemd spell them.
const (
	// rtMinName is the range's first signal.
	rtMinName string = "SIGRTMIN"
	// rtMaxName is the range's last signal.
	rtMaxName string = "SIGRTMAX"
)

// Signal is a typed, platform-portable OS signal. Its underlying value is the
// platform signal number, so Signal(syscall.SIGTERM) and a Signal parsed from
// "SIGTERM" compare equal. The zero value is the reserved invalid signal.
type Signal int

// OS bridges s to the os.Signal expected by os/signal and Process.Signal. It is
// a zero-cost conversion to syscall.Signal — the canonical os.Signal carrier.
func (s Signal) OS() os.Signal {
	//: syscall.Signal is the portable os.Signal implementation across platforms.
	return syscall.Signal(s)
}

// Int reports the raw platform signal number underlying s.
func (s Signal) Int() int {
	//: direct read of the underlying platform signal number.
	return int(s)
}

// String reports the canonical upper-case name of s (e.g. "SIGTERM"). A
// realtime signal renders as "SIGRTMIN" or "SIGRTMIN+n" on Linux and FreeBSD.
// An unrecognised signal renders as "signal <n>" so logs stay unambiguous.
func (s Signal) String() string {
	//: a known signal resolves to its canonical SIG* name.
	if name, ok := signalNames[s]; ok {
		//: table hit — return the canonical name verbatim.
		return name
	}
	//: a realtime signal is named by its offset from SIGRTMIN.
	if s.realtime() {
		//: SIGRTMIN itself, or SIGRTMIN+n.
		return realtimeName(s)
	}
	//: unknown numbers stay legible rather than collapsing to "SIGUNKNOWN".
	return "signal " + strconv.Itoa(int(s))
}

// Known reports whether s names a signal in the platform table or in its
// realtime range (Linux and FreeBSD).
func (s Signal) Known() bool {
	_, ok := signalNames[s]
	//: membership in the platform table, or in its realtime range.
	return ok || s.realtime()
}

// realtime reports whether s lies in the platform's realtime range.
func (s Signal) realtime() bool {
	//: empty where the platform names no range.
	return s >= rtMin && s <= rtMax
}

// realtimeName renders a realtime signal as SIGRTMIN or SIGRTMIN+n.
func realtimeName(s Signal) string {
	//: the range's first signal has no offset to name.
	if s == rtMin {
		//: the bare name.
		return rtMinName
	}
	//: the offset from the range's first signal.
	return rtMinName + "+" + strconv.Itoa(int(s-rtMin))
}

// parseRealtime reads SIGRTMIN, SIGRTMIN+n, SIGRTMAX and SIGRTMAX-n — the
// spellings kill(1) and systemd use — reporting false for anything else or
// for a signal outside the platform's range.
func parseRealtime(canonical string) (sig Signal, ok bool) {
	base, rest, step, named := realtimeEnd(canonical)
	//: not a realtime spelling.
	if !named {
		//: nothing to resolve.
		return 0, false
	}
	offset, counted := realtimeOffset(rest, step)
	candidate := base + Signal(offset)
	//: inside the range, or no signal at all.
	return candidate, counted && candidate.realtime()
}

// realtimeEnd splits a realtime spelling into the end of the range it counts
// from, what follows the name, and the direction it counts in.
func realtimeEnd(canonical string) (base Signal, rest string, step int, ok bool) {
	//: an offset counted up from SIGRTMIN.
	if after, found := strings.CutPrefix(canonical, rtMinName); found {
		//: upward.
		return rtMin, after, 1, true
	}
	//: an offset counted down from SIGRTMAX.
	if after, found := strings.CutPrefix(canonical, rtMaxName); found {
		//: downward.
		return rtMax, after, -1, true
	}
	//: neither name.
	return 0, "", 0, false
}

// realtimeOffset reads the "+n" (step 1) or "-n" (step -1) after a realtime
// name as a signed offset; an empty rest is the range's own end.
func realtimeOffset(rest string, step int) (offset int, ok bool) {
	//: SIGRTMIN or SIGRTMAX alone.
	if rest == "" {
		//: no offset.
		return 0, true
	}
	op := "+"
	//: SIGRTMAX counts down.
	if step < 0 {
		op = "-"
	}
	digits, found := strings.CutPrefix(rest, op)
	n, err := strconv.Atoi(digits)
	//: only the range's own direction and a plain count, no sign, no padding.
	if !found || err != nil || digits != strconv.Itoa(n) || n < 0 {
		//: not an offset.
		return 0, false
	}
	//: signed by the direction.
	return step * n, true
}

// Parse resolves a signal from its name or number into sig. It accepts the
// canonical "SIGTERM", the bare "TERM", and the numeric "15" (all
// case-insensitive), returning UnknownSignal for anything the platform table
// does not define. On Linux (SIGRTMIN 34 to SIGRTMAX 64, glibc's view) and
// FreeBSD (65 to 126) it also accepts the realtime signals as kill(1) and
// systemd spell them — "SIGRTMIN", "SIGRTMIN+3", "SIGRTMAX-2", "RTMIN+1" —
// and their numbers; elsewhere the same spellings are UnknownSignal.
func Parse(name string) (sig Signal, err error) {
	//: trim surrounding space so values lifted from config files parse cleanly.
	trimmed := strings.TrimSpace(name)

	//: an all-digit token is a raw signal number; accept it if the table knows it.
	if n, convErr := strconv.Atoi(trimmed); convErr == nil {
		candidate := Signal(n)
		//: a number outside the platform table is a caller error, not a guess.
		if !candidate.Known() {
			//: reject an unknown number rather than fabricate a signal.
			return 0, UnknownSignal
		}
		//: a known numeric signal is accepted as-is.
		return candidate, nil
	}

	//: normalise to the canonical SIG* form the name table is keyed by.
	canonical := "SIG" + strings.TrimPrefix(strings.ToUpper(trimmed), "SIG")
	//: a realtime spelling resolves by arithmetic, not by the table.
	if rt, ok := parseRealtime(canonical); ok {
		//: inside the platform's range.
		return rt, nil
	}

	//: scan the (small, ~28-entry) table for the canonical name; no reverse map
	//: is kept so the platform files declare a single source of truth.
	for value, named := range signalNames {
		//: an exact canonical-name match resolves the signal.
		if named == canonical {
			//: matched the canonical name — return its Signal.
			return value, nil
		}
	}
	//: no numeric and no name match — the input names no known signal.
	return 0, UnknownSignal
}
