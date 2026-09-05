// Package sdnotify_test — black-box tests for the public sd_notify facade.
package sdnotify_test

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/sdnotify"
)

// TestNotifyNoopWhenUnset asserts the facade no-ops (nil) when NOTIFY_SOCKET is
// unset, delegating the libsystemd semantics intact.
func TestNotifyNoopWhenUnset(t *testing.T) {
	type tc struct {
		name string
		send func() error
	}
	tests := []tc{
		{"Ready", sdnotify.Ready},
		{"Notify with a state map", func() error { return sdnotify.Notify(map[string]string{"READY": "1"}) }},
		{"Notify with an empty map", func() error { return sdnotify.Notify(map[string]string{}) }},
		{"Notify with a nil map", func() error { return sdnotify.Notify(nil) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: an empty NOTIFY_SOCKET counts as "unset"; a service started outside
		//: systemd must not fail merely for reporting its readiness.
		t.Setenv("NOTIFY_SOCKET", "")
		if err := c.send(); err != nil {
			t.Fatalf("%s with no socket = %v, want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestWatchdogInterval asserts the facade parses $WATCHDOG_USEC into a Duration.
func TestWatchdogInterval(t *testing.T) {
	type tc struct {
		name   string
		usec   string
		want   time.Duration
		wantOK bool
	}
	tests := []tc{
		{"ten seconds", "10000000", 10 * time.Second, true},
		{"one second", "1000000", time.Second, true},
		{"a sub-second interval", "500000", 500 * time.Millisecond, true},
		{"unset disables the watchdog", "", 0, false},
		//: a value systemd would never write must disable rather than panic
		//: or yield a nonsense deadline the service then races.
		{"a non-numeric value", "soon", 0, false},
		{"zero disables it", "0", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv("WATCHDOG_USEC", c.usec)
		got, ok := sdnotify.WatchdogInterval()
		if ok != c.wantOK {
			t.Fatalf("WatchdogInterval() ok = %v, want %v", ok, c.wantOK)
		}
		if ok && got != c.want {
			t.Errorf("WatchdogInterval() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestWatchdogIntervalUnset asserts ok is false when $WATCHDOG_USEC is unset.
func TestWatchdogIntervalUnset(t *testing.T) {
	//: ensure the variable is absent.
	t.Setenv("WATCHDOG_USEC", "")
	//: an unset value disables the watchdog.
	_, ok := sdnotify.WatchdogInterval()
	//: ok must be false.
	if ok {
		//: report the unexpected enabled watchdog.
		t.Fatalf("WatchdogInterval() ok = true, want false when unset")
	}
}
