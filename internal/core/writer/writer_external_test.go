package writer_test

import (
	"context"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
)

// stubSink is a no-op Sink returned by the fake factory in these tests.
type stubSink struct{}

func (stubSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: report the byte count so the contract looks like a real terminal sink.
	return len(p), nil
}
func (stubSink) Flush(_ context.Context) error { return nil }
func (stubSink) Close() error                  { return nil }

// fakeFactory is a configurable Factory: it returns sink/err verbatim from
// Open, letting a test drive both the happy and the failure arm.
type fakeFactory struct {
	id   writer.Name
	sink corelogger.Sink
	err  error
}

// : compile-time proof the fake satisfies the Factory port.
var _ writer.Factory = (*fakeFactory)(nil)

func (f *fakeFactory) Name() writer.Name { return f.id }

func (f *fakeFactory) Open(_ writer.Config) (corelogger.Sink, error) {
	//: hand back whatever the test configured.
	return f.sink, f.err
}

func TestName_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   writer.Name
		want string
	}
	tests := []tc{
		{"non-empty round-trips", writer.Name("console"), "console"},
		{"empty round-trips", writer.Name(""), ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: String must return the raw identifier unchanged.
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestName_Known(t *testing.T) {
	//: sequential — registers into the process-wide registry.
	writer.ResetForTest()
	writer.Register(&fakeFactory{id: "known-name", sink: stubSink{}})
	type tc struct {
		name string
		in   writer.Name
		want bool
	}
	tests := []tc{
		{"registered name is known", writer.Name("known-name"), true},
		{"empty name is never known", writer.Name(""), false},
		{"absent name is not known", writer.Name("absent-name"), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Known delegates to Lookup — verify the verdict matches.
		if got := c.in.Known(); got != c.want {
			t.Errorf("%s: Known()=%v want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
