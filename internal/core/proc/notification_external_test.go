package proc_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// predicateCase is one datagram and the answer each predicate owes it.
type predicateCase struct {
	name  string
	state map[string]string
	want  [4]bool // Ready, Reloading, Stopping, Watchdog
}

// predicateCasesFor returns the datagrams every predicate test reads. They
// differ only in which of the four answers they check, so stating the
// datagrams once keeps them from drifting apart.
func predicateCasesFor() []predicateCase {
	return []predicateCase{
		{"a nil state answers false throughout", nil, [4]bool{}},
		{"an empty state answers false throughout", map[string]string{}, [4]bool{}},
		{"READY=1", map[string]string{"READY": "1"}, [4]bool{true}},
		{"RELOADING=1", map[string]string{"RELOADING": "1"}, [4]bool{false, true}},
		{"STOPPING=1", map[string]string{"STOPPING": "1"}, [4]bool{false, false, true}},
		{"WATCHDOG=1", map[string]string{"WATCHDOG": "1"}, [4]bool{false, false, false, true}},
		// Only the exact "1" counts: sd_notify(3) defines the value, and accepting
		// anything truthy would let "0" read as ready.
		{"READY=0 is not ready", map[string]string{"READY": "0"}, [4]bool{}},
		{"READY with an unexpected value", map[string]string{"READY": "yes"}, [4]bool{}},
		{
			"a datagram can carry several flags",
			map[string]string{"READY": "1", "WATCHDOG": "1"},
			[4]bool{true, false, false, true},
		},
	}
}

// A nil State must answer false rather than panic: a datagram that parsed to
// nothing is simply not ready, and a supervisor should not have to guard every
// call.
func Test_NotificationValue_Ready(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c predicateCase) {
		t.Helper()
		if got := (coreproc.NotificationValue{State: c.state}).Ready(); got != c.want[0] {
			t.Errorf("Ready() = %v, want %v", got, c.want[0])
		}
	}
	tests := predicateCasesFor()
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Reloading reads through the same State map, so the same nil-safety holds.
func Test_NotificationValue_Reloading(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c predicateCase) {
		t.Helper()
		if got := (coreproc.NotificationValue{State: c.state}).Reloading(); got != c.want[1] {
			t.Errorf("Reloading() = %v, want %v", got, c.want[1])
		}
	}
	tests := predicateCasesFor()
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Stopping reads through the same State map.
func Test_NotificationValue_Stopping(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c predicateCase) {
		t.Helper()
		if got := (coreproc.NotificationValue{State: c.state}).Stopping(); got != c.want[2] {
			t.Errorf("Stopping() = %v, want %v", got, c.want[2])
		}
	}
	tests := predicateCasesFor()
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Watchdog reads through the same State map; it is the keep-alive ping.
func Test_NotificationValue_Watchdog(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, c predicateCase) {
		t.Helper()
		if got := (coreproc.NotificationValue{State: c.state}).Watchdog(); got != c.want[3] {
			t.Errorf("Watchdog() = %v, want %v", got, c.want[3])
		}
	}
	tests := predicateCasesFor()
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Status, MainPID and SenderPID carry no policy: they are what the datagram
// said, with SenderPID the one field a sender cannot forge.
func Test_NotificationValue_fields(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value coreproc.NotificationValue
		want  coreproc.NotificationValue
	}
	tests := []tc{
		{
			"a full datagram",
			coreproc.NotificationValue{Status: "serving", MainPID: 42, SenderPID: 42},
			coreproc.NotificationValue{Status: "serving", MainPID: 42, SenderPID: 42},
		},
		{
			// SenderPID is 0 when SO_PASSCRED gave nothing, which a supervisor
			// must be able to tell apart from a real PID.
			"credentials unavailable",
			coreproc.NotificationValue{Status: "serving", MainPID: 42},
			coreproc.NotificationValue{Status: "serving", MainPID: 42, SenderPID: 0},
		},
		{"an empty datagram", coreproc.NotificationValue{}, coreproc.NotificationValue{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if c.value.Status != c.want.Status {
			t.Errorf("Status = %q, want %q", c.value.Status, c.want.Status)
		}
		if c.value.MainPID != c.want.MainPID {
			t.Errorf("MainPID = %d, want %d", c.value.MainPID, c.want.MainPID)
		}
		if c.value.SenderPID != c.want.SenderPID {
			t.Errorf("SenderPID = %d, want %d", c.value.SenderPID, c.want.SenderPID)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
