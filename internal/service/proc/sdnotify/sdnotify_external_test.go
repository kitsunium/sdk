// Package sdnotify_test — the portable half of the protocol.
package sdnotify_test

import (
	"testing"
	"time"

	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// TestWatchdogInterval pins the reader a service calls to decide its ping
// period.
//
// It touches no socket — only the environment — so it works identically on every
// platform, and a service does not have to know whether the listener half exists
// there. The "disabled" answers matter as much as the enabled ones: a value
// systemd would never write must disable the watchdog rather than produce a
// nonsense deadline the service then races, and it must report the ZERO duration
// so a caller that ignored ok sleeps forever instead of pinging in a tight loop.
func TestWatchdogInterval(t *testing.T) {
	//: not parallel — every case sets $WATCHDOG_USEC.
	type tc struct {
		name   string
		usec   string
		want   time.Duration
		wantOK bool
	}
	tests := []tc{
		{name: "ten seconds", usec: "10000000", want: 10 * time.Second, wantOK: true},
		{name: "one second", usec: "1000000", want: time.Second, wantOK: true},
		{name: "a sub-second interval", usec: "500000", want: 500 * time.Millisecond, wantOK: true},
		{name: "unset disables the watchdog", usec: ""},
		{name: "a non-numeric value", usec: "soon"},
		{name: "zero", usec: "0"},
		{name: "a negative value", usec: "-1"},
		{name: "a value with whitespace", usec: " 1000000 "},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("WATCHDOG_USEC", c.usec)

		got, ok := svcsdnotify.WatchdogInterval()
		if ok != c.wantOK {
			t.Fatalf("WatchdogInterval() ok = %v, want %v", ok, c.wantOK)
		}
		if ok && got != c.want {
			t.Errorf("WatchdogInterval() = %v, want %v", got, c.want)
		}
		//: a disabled watchdog reports zero, so a caller that ignored ok sleeps
		//: forever rather than pinging in a tight loop.
		if !ok && got != 0 {
			t.Errorf("WatchdogInterval() = %v with ok=false, want 0", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
