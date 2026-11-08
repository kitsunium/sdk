package notify

import (
	"strconv"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxNameBytes bounds FDNAME=, BUSERROR= and VARLINKERROR=: systemd's
// FDNAME_MAX, the longest descriptor name LISTEN_FDNAMES carries.
const maxNameBytes int = 255

// pidBits sizes a PID: a pid_t, positive.
const pidBits int = 31

// assignmentRules bounds the value of each assignment sd_notify(3) defines
// (kitsunium/sdk#730): a flag is 1, a PID positive and decimal, a count of
// microseconds decimal, a name printable. A name it does not list is not
// sd_notify's to bound and is kept as it came.
var assignmentRules = map[string]func(string) bool{
	"READY":               isOne,
	"RELOADING":           isOne,
	"STOPPING":            isOne,
	"FDSTORE":             isOne,
	"FDSTOREREMOVE":       isOne,
	"BARRIER":             isOne,
	"MAINPIDFD":           isOne,
	"RESTART_RESET":       isOne,
	"STATUS":              utf8.ValidString,
	"EXIT_STATUS":         utf8.ValidString,
	"MAINPID":             isPID,
	"WATCHDOG":            isWatchdog,
	"WATCHDOG_USEC":       isUint64,
	"EXTEND_TIMEOUT_USEC": isUint64,
	"MONOTONIC_USEC":      isUint64,
	"MAINPIDFDID":         isUint64,
	"ERRNO":               isErrno,
	"BUSERROR":            isErrorName,
	"VARLINKERROR":        isErrorName,
	"NOTIFYACCESS":        isNotifyAccess,
	"FDNAME":              fdNameValid,
	"FDPOLL":              isBoolDigit,
}

// checkAssignments refuses a datagram one of whose assignments sd_notify(3)
// defines carries a value out of its bounds, naming the assignment. An empty
// name is no assignment at all.
func checkAssignments(state map[string]string) error {
	//: every assignment the datagram carries.
	for name, value := range state {
		//: "=value" names nothing.
		if name == "" {
			//: INVALID_NOTIFICATION naming the empty name.
			return wrapInvalid(nil, errs.String("name", name))
		}
		rule, defined := assignmentRules[name]
		//: a defined assignment, out of its bounds.
		if defined && !rule(value) {
			//: INVALID_NOTIFICATION naming the assignment.
			return wrapInvalid(nil, errs.String("name", name), errs.String("value", value))
		}
	}
	//: every defined assignment within bounds.
	return nil
}

// fdNameValid reports whether name may name a descriptor in LISTEN_FDNAMES or
// FDNAME=, as systemd's fdname_is_valid decides: at most 255 bytes, each
// printable ASCII, none a ':' — the list's separator. The empty name is valid.
func fdNameValid(name string) bool {
	//: systemd's FDNAME_MAX.
	if len(name) > maxNameBytes {
		//: too long.
		return false
	}
	//: byte by byte: ASCII only.
	for i := range len(name) {
		c := name[i]
		//: a control byte, DEL or beyond, or the separator.
		if c < ' ' || c > '~' || c == ':' {
			//: refused.
			return false
		}
	}
	//: printable, separator-free.
	return true
}

// isOne reports whether a flag is set: sd_notify's flags are 1 or absent.
func isOne(v string) bool {
	//: only 1.
	return v == "1"
}

// isPID reports whether v is a positive decimal PID.
func isPID(v string) bool {
	n, err := strconv.ParseUint(v, decimalBase, pidBits)
	//: decimal, no sign, positive.
	return err == nil && n > 0
}

// isUint64 reports whether v is an unsigned decimal 64-bit count.
func isUint64(v string) bool {
	_, err := strconv.ParseUint(v, decimalBase, int64Bits)
	//: decimal, no sign.
	return err == nil
}

// isErrno reports whether v is an errno: a non-negative decimal int.
func isErrno(v string) bool {
	_, err := strconv.ParseUint(v, decimalBase, pidBits)
	//: decimal, no sign.
	return err == nil
}

// isWatchdog reports whether v is a keep-alive (1) or a trigger.
func isWatchdog(v string) bool {
	//: the two values sd_notify(3) defines.
	return v == "1" || v == "trigger"
}

// isBoolDigit reports whether v is 0 or 1.
func isBoolDigit(v string) bool {
	//: FDPOLL=0 disables the poll; 1 is the default restated.
	return v == "0" || v == "1"
}

// isNotifyAccess reports whether v is a NotifyAccess= value.
func isNotifyAccess(v string) bool {
	//: the four accesses.
	switch v {
	//: known.
	case "none", "main", "exec", "all":
		//: valid.
		return true
	//: anything else.
	default:
		//: refused.
		return false
	}
}

// isErrorName reports whether v can be a D-Bus or Varlink error name: 1 to
// 255 printable ASCII bytes, no blank.
func isErrorName(v string) bool {
	//: empty or too long.
	if v == "" || len(v) > maxNameBytes {
		//: refused.
		return false
	}
	//: byte by byte.
	for i := range len(v) {
		//: a control, a blank, DEL or beyond.
		if v[i] <= ' ' || v[i] > '~' {
			//: refused.
			return false
		}
	}
	//: a name.
	return true
}
