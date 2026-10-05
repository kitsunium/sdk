package proc

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// os is Signal.OS's body: decl_gen.go writes Signal.OS, from the
// design, as one call of it.
func (s Signal) os() os.Signal {
	//: syscall.Signal is the portable os.Signal implementation across platforms.
	return syscall.Signal(s)
}

// Int reports the raw platform signal number underlying s.
func (s Signal) Int() int {
	//: direct read of the underlying platform signal number.
	return int(s)
}

// String reports the canonical upper-case name of s (e.g. "SIGTERM"). An
// unrecognised signal renders as "signal <n>" so logs stay unambiguous.
func (s Signal) String() string {
	//: a known signal resolves to its canonical SIG* name.
	if name, ok := signalNames[s]; ok {
		//: table hit — return the canonical name verbatim.
		return name
	}
	//: unknown numbers stay legible rather than collapsing to "SIGUNKNOWN".
	return "signal " + strconv.Itoa(int(s))
}

// known is Signal.Known's body: decl_gen.go writes Signal.Known, from the
// design, as one call of it.
func (s Signal) known() bool {
	_, ok := signalNames[s]
	//: membership in the platform table is the definition of a known signal.
	return ok
}

// Parse resolves a signal from its name or number into sig. It accepts the
// canonical "SIGTERM", the bare "TERM", and the numeric "15" (all
// case-insensitive), returning UnknownSignal for anything the platform table
// does not define.
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
