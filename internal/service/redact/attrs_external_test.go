package redact_test

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/service/redact"
)

// attr is shorthand for a log attribute.
func attr(key string, value corelogger.Value) corelogger.AttrValue {
	return corelogger.AttrValue{Key: key, Value: value}
}

// stringer is a type with its own rendering.
type stringer struct{}

// String renders it.
func (stringer) String() string { return "rendered by String" }

// declared is a type carrying a declared secret, logged with logger.Any.
type declared struct {
	Visible string `json:"visible"`
	Hidden  string `json:"hidden" redact:"secret"`
}

// collect ranges over every pair.
func collect(r *redact.Redactor, attrs []corelogger.AttrValue, maxBytes int) map[string]string {
	return maps.Collect(r.Attrs(attrs, maxBytes))
}

// TestAttrsRendersARecordForDisplay pins the rendering a log panel shows: a
// group flattened into dotted keys, a secret's name hiding what it holds — a
// whole group included, as ONE pair — each kind written as Go writes it, and
// what logger.Any carried rendered by its own rules.
func TestAttrsRendersARecordForDisplay(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 24, 10, 0, 0, 5, time.FixedZone("", 7200))
	attrs := []corelogger.AttrValue{
		attr("user", corelogger.StringValue("ann")),
		attr("db", corelogger.GroupValue(
			attr("host", corelogger.StringValue("db.local")),
			attr("password", corelogger.StringValue("hunter2")),
			attr("dsn", corelogger.StringValue("postgres://app:hunter2@db/x")),
		)),
		attr("session", corelogger.GroupValue(attr("id", corelogger.StringValue("s-1")), attr("user", corelogger.StringValue("ann")))),
		attr("count", corelogger.Int64Value(-3)),
		attr("size", corelogger.Uint64Value(7)),
		attr("ratio", corelogger.Float64Value(0.25)),
		attr("ok", corelogger.BoolValue(true)),
		attr("took", corelogger.DurationValue(1500*time.Millisecond)),
		attr("at", corelogger.TimeValue(at)),
		attr("err", corelogger.AnyValue(errors.New("dial https://u:hunter2@h failed"))),
		attr("thing", corelogger.AnyValue(stringer{})),
		attr("model", corelogger.AnyValue(declared{Visible: "v", Hidden: "hunter2"})),
		attr("nothing", corelogger.AnyValue(nil)),
		attr("stream", corelogger.AnyValue(make(chan int))),
	}
	want := map[string]string{
		"user": "ann", "db.host": "db.local", "db.password": redact.Placeholder,
		"db.dsn": "postgres://[redacted]@db/x", "session": redact.Placeholder,
		"count": "-3", "size": "7", "ratio": "0.25", "ok": "true", "took": "1.5s",
		"at": "2026-09-24T08:00:00.000000005Z", "err": "dial https://[redacted]@h failed",
		"thing": "rendered by String", "model": `{"visible":"v","hidden":"[redacted]"}`,
		"nothing": "null", "stream": redact.Unencodable,
	}
	got := collect(redact.NewRedactor(redact.Config{}), attrs, 1024)
	for key, text := range want {
		if got[key] != text {
			t.Errorf("%s = %q, want %q", key, got[key], text)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d pairs, want %d: %v", len(got), len(want), got)
	}
	for key, text := range got {
		if strings.Contains(text, "hunter2") {
			t.Errorf("%s showed a secret: %q", key, text)
		}
	}
}

// TestAttrsUsesTheCallersErrorRendering pins Config.Error: a framework that
// shows only an error's wire-safe half passes its own renderer, and the
// result is still scrubbed and cut.
func TestAttrsUsesTheCallersErrorRendering(t *testing.T) {
	t.Parallel()
	r := redact.NewRedactor(redact.Config{Error: func(error) string { return "internal error at https://x:y@h" }})
	got := collect(r, []corelogger.AttrValue{attr("err", corelogger.AnyValue(errors.New("the real cause")))}, 1024)
	if got["err"] != "internal error at https://[redacted]@h" {
		t.Errorf("err = %q", got["err"])
	}
}

// TestAttrsStopsWhenTheCallerDoes pins why it is an iterator: the caller
// bounds the count, and a group of ten thousand costs what the caller ranges.
func TestAttrsStopsWhenTheCallerDoes(t *testing.T) {
	t.Parallel()
	members := make([]corelogger.AttrValue, 10_000)
	for index := range members {
		members[index] = attr("k", corelogger.Int64Value(int64(index)))
	}
	seen := 0
	for range redact.NewRedactor(redact.Config{}).Attrs([]corelogger.AttrValue{attr("big", corelogger.GroupValue(members...))}, 64) {
		seen++
		if seen == 32 {
			break
		}
	}
	if seen != 32 {
		t.Errorf("ranged %d pairs, want to stop at 32", seen)
	}
}

// TestAttrsBoundsEachText pins the per-text bound, for a string and for the
// JSON of an opaque value alike — the JSON stays well-formed.
func TestAttrsBoundsEachText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 500)
	got := collect(redact.NewRedactor(redact.Config{}), []corelogger.AttrValue{
		attr("text", corelogger.StringValue(long)),
		attr("model", corelogger.AnyValue(declared{Visible: long})),
	}, 64)
	for key, text := range got {
		if len(text) > 64 {
			t.Errorf("%s is %d bytes over a bound of 64", key, len(text))
		}
	}
	if !strings.HasSuffix(got["text"], redact.Ellipsis) {
		t.Errorf("a cut text does not say so: %q", got["text"])
	}
}
