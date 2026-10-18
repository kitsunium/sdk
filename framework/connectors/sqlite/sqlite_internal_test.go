package sqlite

import (
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
)

// The engine waits for the file's write lock as long as kit says.
var _ app.LockWaiter = engine{}

// The engine's defaults fill what the URL leaves out, and a parameter the
// URL writes wins — under either spelling the driver reads. The busy
// timeout kit gives (ADR 0178 C2) replaces the engine's own five seconds,
// never the URL's.
func TestTheURLsParametersWin(t *testing.T) {
	for _, c := range []struct {
		raw  string
		wait time.Duration
		want map[string]string
	}{
		{"/data/archive.sqlite", 0, map[string]string{"_journal_mode": "WAL", "_busy_timeout": "5000", "_txlock": "immediate"}},
		{"/data/archive.sqlite", 4500 * time.Millisecond, map[string]string{"_journal_mode": "WAL", "_busy_timeout": strconv.FormatInt(busyTimeout(4500*time.Millisecond).Milliseconds(), 10), "_txlock": "immediate"}},
		{"file:/data/archive.sqlite?_busy_timeout=9000&_foreign_keys=1", 4500 * time.Millisecond, map[string]string{
			"_journal_mode": "WAL", "_busy_timeout": "9000", "_txlock": "immediate", "_foreign_keys": "1",
		}},
		{"file:/data/archive.sqlite?_timeout=900&_journal=DELETE", 4500 * time.Millisecond, map[string]string{
			"_journal": "DELETE", "_timeout": "900", "_txlock": "immediate",
		}},
	} {
		got, err := dsn(c.raw, c.wait)
		if err != nil {
			t.Fatalf("%q: %v", c.raw, err)
		}
		path, rawQuery, _ := strings.Cut(got, "?")
		query, err := url.ParseQuery(rawQuery)
		if err != nil || path != "/data/archive.sqlite" || len(query) != len(c.want) {
			t.Fatalf("%q, %s opens %q", c.raw, c.wait, got)
		}
		for key, value := range c.want {
			if query.Get(key) != value {
				t.Errorf("%q, %s: %s is %q, want %q", c.raw, c.wait, key, query.Get(key), value)
			}
		}
	}
	if _, err := dsn("file:?x=1", 0); err == nil {
		t.Error("a URL without a file")
	}
}

// SQLite counts the busy sleeps it asked for, not the time that passed: the
// timeout handed to it is kit's wait where a sleep ends when asked, and where
// each ends late, the longest one whose sleeps and their overshoot still end
// within the wait — one millisecond more would not.
func TestTheBusyTimeoutEndsWithinTheWaitOfWallTime(t *testing.T) {
	if got := busySleeps(1750 * time.Millisecond); got != 27 {
		t.Errorf("SQLite sleeps %d times in 1.75 s, want 27 (twelve, then 100 ms each, the last cut)", got)
	}
	for _, wait := range []time.Duration{250 * time.Millisecond, 1750 * time.Millisecond, 4500 * time.Millisecond, 27 * time.Second} {
		if got := busyTimeoutWith(wait, 0); got != wait {
			t.Errorf("busyTimeoutWith(%s, 0) = %s, want the wait itself", wait, got)
		}
		const late = 12 * time.Millisecond
		got := busyTimeoutWith(wait, late)
		if spent := got + time.Duration(busySleeps(got))*late; spent > wait {
			t.Errorf("busyTimeoutWith(%s, %s) = %s, which ends after %s", wait, late, got, spent)
		}
		next := got + time.Millisecond
		if spent := next + time.Duration(busySleeps(next))*late; spent <= wait {
			t.Errorf("busyTimeoutWith(%s, %s) = %s, and %s would still end by %s", wait, late, got, next, spent)
		}
	}
	switch runtime.GOOS {
	case "openbsd", "netbsd":
		if sleepOvershoot() == 0 {
			t.Errorf("%s's sleeps end on a 10 ms tick, and the overshoot is not counted", runtime.GOOS)
		}
	default:
		if busyTimeout(4500*time.Millisecond) != 4500*time.Millisecond {
			t.Errorf("busyTimeout on %s = %s, want kit's wait itself", runtime.GOOS, busyTimeout(4500*time.Millisecond))
		}
	}
}
