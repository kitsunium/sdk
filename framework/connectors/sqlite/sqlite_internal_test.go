package sqlite

import (
	"net/url"
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
		{"/data/archive.sqlite", 4500 * time.Millisecond, map[string]string{"_journal_mode": "WAL", "_busy_timeout": "4500", "_txlock": "immediate"}},
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
