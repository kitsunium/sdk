package sqlite

import (
	"net/url"
	"strings"
	"testing"
)

// The engine's defaults fill what the URL leaves out, and a parameter the
// URL writes wins.
func TestTheURLsParametersWin(t *testing.T) {
	for raw, want := range map[string]map[string]string{
		"/data/archive.sqlite": {"_journal_mode": "WAL", "_busy_timeout": "5000", "_txlock": "immediate"},
		"file:/data/archive.sqlite?_busy_timeout=9000&_foreign_keys=1": {
			"_journal_mode": "WAL", "_busy_timeout": "9000", "_txlock": "immediate", "_foreign_keys": "1",
		},
	} {
		got, err := dsn(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		path, rawQuery, _ := strings.Cut(got, "?")
		query, err := url.ParseQuery(rawQuery)
		if err != nil || path != "/data/archive.sqlite" || len(query) != len(want) {
			t.Fatalf("%q opens %q", raw, got)
		}
		for key, value := range want {
			if query.Get(key) != value {
				t.Errorf("%q: %s is %q, want %q", raw, key, query.Get(key), value)
			}
		}
	}
	if _, err := dsn("file:?x=1"); err == nil {
		t.Error("a URL without a file")
	}
}
