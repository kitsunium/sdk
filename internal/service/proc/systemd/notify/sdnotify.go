package notify

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// envNotifySocket is the environment variable naming the notify datagram socket.
const envNotifySocket = "NOTIFY_SOCKET"

// envWatchdogUsec is the environment variable carrying the watchdog timeout, in
// microseconds, that a supervisor sets for its child.
const envWatchdogUsec = "WATCHDOG_USEC"

// envWatchdogPID is the environment variable naming the one process the
// watchdog timeout is meant for; a child inheriting the environment is not it.
const envWatchdogPID = "WATCHDOG_PID"

// exitDataErr mirrors EX_DATAERR (sysexits.h): a received datagram was malformed.
// It matches the InvalidNotification sentinel's exit code in core/proc.
const exitDataErr int = 65

// exitOSErr mirrors EX_OSERR (sysexits.h): an OS-level send/socket op failed. It
// matches the NotifyFailed / ListenFailed sentinels' exit code in core/proc.
const exitOSErr int = 71

// exitNoPerm mirrors EX_NOPERM (sysexits.h): a credential check failed. It
// matches the CredentialMismatch sentinel's exit code in core/proc.
const exitNoPerm int = 77

// decimalBase is the radix for parsing protocol integer fields (MAINPID,
// WATCHDOG_USEC), all base-10 per the sd_notify spec.
const decimalBase int = 10

// int64Bits sizes strconv.ParseInt for the 64-bit WATCHDOG_USEC microsecond count.
const int64Bits int = 64

// maxWatchdogUsec is the largest $WATCHDOG_USEC a time.Duration can hold: past
// it, usec * time.Microsecond wraps, and 9223372036854775807 read as -1µs with
// ok true — a ticker built on half of it panics.
const maxWatchdogUsec int64 = math.MaxInt64 / int64(time.Microsecond)

// wrapInvalid restates the InvalidNotification sentinel fields around a cause,
// attaching the offending field for diagnostics.
func wrapInvalid(cause error, field errs.FieldValue) error {
	//: copy the exact sentinel strings so the wrapped error matches the code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeInvalidNotification,
		Reason:   "INVALID_NOTIFICATION",
		Public:   "Received sd_notify datagram is malformed",
		Private:  "service/proc/systemd/notify.Recv: datagram did not parse into NAME=value fields",
		ExitCode: exitDataErr,
	}, field)
}

// resolveAddr turns a $NOTIFY_SOCKET value into the AF_UNIX name to bind/connect:
// a leading '@' marks the abstract namespace and is replaced by a NUL byte so the
// kernel treats sun_path[0]==0 as abstract.
func resolveAddr(raw string) string {
	//: an abstract-namespace address starts with '@'; swap it for the NUL the
	//: kernel actually expects in sun_path[0].
	if strings.HasPrefix(raw, "@") {
		//: replace only the leading marker, preserving the rest of the name.
		return "\x00" + raw[1:]
	}
	//: a pathname socket is used verbatim.
	return raw
}

// encodePayload joins a NAME=value state map into the newline-separated datagram
// body sd_notify expects. Order is not significant to the protocol. A name or
// value carrying the '\n' field delimiter (or a name carrying '=', the
// name/value delimiter) would forge extra fields in the datagram, so such a
// state is rejected with InvalidNotification rather than encoded.
func encodePayload(state map[string]string) (body string, err error) {
	//: deliberately NOT pre-sized. Computing the exact size needs a second range
	//: over the map, and a map range costs ~60 ns of iterator setup — more than
	//: the single growth it would save on the one-field payload every shorthand
	//: (Ready/Watchdog/Status/MainPID) builds. Measured both ways in BENCH.md:
	//: pre-sizing made Ready() 29 % SLOWER.
	var b strings.Builder
	//: emit one "NAME=value" line per entry; the trailing newline per line is
	//: harmless and matches systemd's own framing.
	for name, value := range state {
		//: a name containing '\n' or '=' would split into forged extra fields. Two
		//: byte searches, not ContainsAny: both delimiters are ASCII, so the results
		//: are identical for every input (a UTF-8 continuation byte is never 0x0A or
		//: 0x3D), and ContainsAny decodes a rune per byte for a short name — 24 % of
		//: this function, named by the profile in BENCH.md.
		if strings.Contains(name, "\n") || strings.Contains(name, "=") {
			//: reject the field-injecting name as a malformed notification.
			return "", wrapInvalid(nil, errs.String("name", name))
		}
		//: a value containing '\n' would inject additional NAME=value lines.
		if strings.ContainsRune(value, '\n') {
			//: reject the field-injecting value as a malformed notification.
			return "", wrapInvalid(nil, errs.String("value", value))
		}
		//: each field is its own line in the environment-block-style body.
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	//: the assembled multi-line body is the datagram payload.
	return b.String(), nil
}

// parsePayload splits a received datagram body into a NotificationValue, lifting
// STATUS / MAINPID into their typed fields. It returns InvalidNotification when a
// non-empty line carries no '=' or MAINPID is not a base-10 integer.
func parsePayload(body string) (value coreproc.NotificationValue, err error) {
	//: State is always non-nil so callers can index it without a guard.
	state := make(map[string]string)
	//: walk every newline-delimited field of the environment-block-style body.
	for line := range strings.SplitSeq(body, "\n") {
		//: skip blank lines produced by the trailing newline framing.
		if line == "" {
			//: an empty segment is not a field; ignore it.
			continue
		}
		//: a field MUST be NAME=value; the first '=' separates the two halves.
		name, val, found := strings.Cut(line, "=")
		//: a line without '=' is malformed per the protocol.
		if !found {
			//: report the parse failure with a typed error.
			return coreproc.NotificationValue{}, wrapInvalid(nil, errs.String("line", line))
		}
		//: preserve the raw field verbatim in State.
		state[name] = val
	}
	//: MAINPID, when present, must be a base-10 PID.
	mainPID, err := mainPIDFromState(state)
	//: a non-integer MAINPID is a malformed datagram.
	if err != nil {
		//: propagate the typed InvalidNotification produced by the helper.
		return coreproc.NotificationValue{}, err
	}
	//: assemble the value with the typed STATUS/MAINPID lifted out of State.
	return coreproc.NotificationValue{
		State:   state,
		Status:  state["STATUS"],
		MainPID: mainPID,
	}, nil
}

// mainPIDFromState extracts MAINPID as an int, returning 0 when absent and a
// typed InvalidNotification when present but not a base-10 integer.
func mainPIDFromState(state map[string]string) (pid int, err error) {
	//: an absent MAINPID is valid and yields the zero PID.
	raw, ok := state["MAINPID"]
	//: nothing to parse when the field is missing.
	if !ok {
		//: report the conventional zero PID with no error.
		return 0, nil
	}
	//: a present MAINPID must be a base-10 integer.
	parsed, convErr := strconv.Atoi(raw)
	//: a non-numeric MAINPID makes the whole datagram malformed.
	if convErr != nil {
		//: wrap the conversion fault as InvalidNotification.
		return 0, wrapInvalid(convErr, errs.String("mainpid", raw))
	}
	//: hand back the parsed PID.
	return parsed, nil
}

// watchdogInterval parses a $WATCHDOG_USEC value (microseconds) into a duration,
// for the process whose PID is self, given the $WATCHDOG_PID value pidRaw.
// ok is false when raw is empty, not a positive base-10 integer, or larger than
// a time.Duration holds, and when pidRaw is set and does not name self —
// mirroring sd_watchdog_enabled's "watchdog disabled" result.
func watchdogInterval(raw, pidRaw string, self int) (d time.Duration, ok bool) {
	//: an unset variable means the watchdog is disabled; a $WATCHDOG_PID that
	//: does not name this process means the timeout was meant for another — a
	//: parent whose environment this process inherited.
	if raw == "" || !watchdogIsOurs(pidRaw, self) {
		//: signal "no watchdog" with the zero duration.
		return 0, false
	}
	//: the value is an unsigned count of microseconds.
	usec, err := strconv.ParseInt(raw, decimalBase, int64Bits)
	//: a non-integer, non-positive or unrepresentable value disables the watchdog.
	if err != nil || usec <= 0 || usec > maxWatchdogUsec {
		//: treat a malformed timeout as "disabled" rather than erroring.
		return 0, false
	}
	//: scale microseconds to a Duration; the bound above keeps it positive.
	return time.Duration(usec) * time.Microsecond, true
}

// watchdogIsOurs reports whether a $WATCHDOG_PID value addresses the process
// self. An unset variable addresses whoever reads $WATCHDOG_USEC, as in
// sd_watchdog_enabled(3); a value that is not a PID addresses nobody.
func watchdogIsOurs(pidRaw string, self int) bool {
	//: systemd sets it only alongside a timeout meant for one process.
	if pidRaw == "" {
		//: no addressee named: the timeout is the reader's.
		return true
	}
	pid, err := strconv.Atoi(pidRaw)
	//: sd_watchdog_enabled refuses a malformed PID rather than guessing.
	return err == nil && pid == self
}

// WatchdogInterval reports the watchdog ping interval the supervisor configured
// via $WATCHDOG_USEC. ok is false — sd_watchdog_enabled's "watchdog disabled"
// result — when the variable is unset, malformed, not positive, or larger than
// a time.Duration holds, and when $WATCHDOG_PID is set and does not name this
// process: a child that inherited its parent's environment is not the process
// the supervisor watches. When ok is true, d is positive. It is portable: it
// reads only the environment and never touches a socket.
func WatchdogInterval() (d time.Duration, ok bool) {
	//: delegate the parsing to the shared helper, addressed to this process.
	return watchdogInterval(os.Getenv(envWatchdogUsec), os.Getenv(envWatchdogPID), os.Getpid())
}
