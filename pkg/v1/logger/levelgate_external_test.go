package logger_test

import (
	"context"
	"testing"

	logger "github.com/kitsunium/sdk/pkg/v1/logger"
)

// countingSink counts the lifecycle calls that reach it, so a test can tell a
// delegated Flush or Close from one the gate swallowed.
type countingSink struct {
	memory  *logger.MemorySink
	flushes int
	closes  int
}

// Write keeps the record, as the memory sink does.
func (s *countingSink) Write(ctx context.Context, record logger.Record, payload []byte) (int, error) {
	//: kept, whole.
	return s.memory.Write(ctx, record, payload)
}

// Flush counts, then behaves as the memory sink does.
func (s *countingSink) Flush(ctx context.Context) error {
	s.flushes++
	return s.memory.Flush(ctx)
}

// Close counts, then behaves as the memory sink does.
func (s *countingSink) Close() error {
	s.closes++
	return s.memory.Close()
}

// recordSource is what messages reads: a sink that keeps its records.
type recordSource interface {
	Records() []logger.RecordSnapshot
}

// messages lists what a memory sink received, in order.
func messages(sink recordSource) []string {
	records := sink.Records()
	got := make([]string, 0, len(records))
	//: in arrival order.
	for _, record := range records {
		got = append(got, record.Message)
	}
	return got
}

// TestLevelGateTeesOneLoggerAtTwoFloors is the composition the gate exists
// for: one logger at Debug, one branch gated at Info and one taking everything.
// Info is the floor that matters — the writer configuration's gate reads Info
// as "inherit" and would let Debug through the gated branch.
func TestLevelGateTeesOneLoggerAtTwoFloors(t *testing.T) {
	t.Parallel()
	terminal := logger.NewMemorySink()
	everything := logger.NewMemorySink()
	lg, err := logger.NewWithSink(logger.SinkConfig{
		Sink:     logger.Multi(logger.LevelGate(terminal, logger.LevelInfo), everything),
		MinLevel: logger.LevelDebug,
	})
	//: a valid topology.
	if err != nil {
		t.Fatalf("NewWithSink: %v", err)
	}
	ctx := t.Context()
	logger.Debug(ctx, lg, "debug")
	logger.Info(ctx, lg, "info")
	logger.Warn(ctx, lg, "warn")
	logger.Error(ctx, lg, "error")

	//: the gated branch sees Info and above only.
	if got, want := messages(terminal), []string{"info", "warn", "error"}; !equalStrings(got, want) {
		t.Errorf("gated branch received %v, want %v", got, want)
	}
	//: the other branch sees every level, Debug included.
	if got, want := messages(everything), []string{"debug", "info", "warn", "error"}; !equalStrings(got, want) {
		t.Errorf("ungated branch received %v, want %v", got, want)
	}
}

// TestLevelGateDropReportsSuccess pins that a dropped record is not a failed
// write: every byte reported accepted, no error, nothing delivered.
func TestLevelGateDropReportsSuccess(t *testing.T) {
	t.Parallel()
	inner := logger.NewMemorySink()
	gate := logger.LevelGate(inner, logger.LevelWarn)
	n, err := gate.Write(t.Context(), logger.Record{Level: logger.LevelInfo, Message: "below"}, []byte("payload"))
	//: accepted in full, silently.
	if err != nil || n != len("payload") {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len("payload"))
	}
	//: and never delivered.
	if inner.Len() != 0 {
		t.Fatalf("a record below the floor reached the sink: %v", messages(inner))
	}
}

// TestLevelGateDelegatesFlushAndClose pins that the gate holds nothing of its
// own: closing it closes the sink behind it.
func TestLevelGateDelegatesFlushAndClose(t *testing.T) {
	t.Parallel()
	inner := &countingSink{memory: logger.NewMemorySink()}
	gate := logger.LevelGate(inner, logger.LevelError)
	//: forwarded once.
	if err := gate.Flush(t.Context()); err != nil || inner.flushes != 1 {
		t.Errorf("Flush: err=%v flushes=%d, want nil and 1", err, inner.flushes)
	}
	//: forwarded once.
	if err := gate.Close(); err != nil || inner.closes != 1 {
		t.Errorf("Close: err=%v closes=%d, want nil and 1", err, inner.closes)
	}
}

// TestLevelGateOverNothingIsRefusedDownstream pins the nil case end to end: the
// gate over no sink is nil, a fan-out skips it, and NewWithSink refuses it.
func TestLevelGateOverNothingIsRefusedDownstream(t *testing.T) {
	t.Parallel()
	gate := logger.LevelGate(nil, logger.LevelInfo)
	//: nil, not a gate that would fail on its first record.
	if gate != nil {
		t.Fatalf("LevelGate(nil) = %T, want nil", gate)
	}
	//: refused where a nil sink always is.
	if _, err := logger.NewWithSink(logger.SinkConfig{Sink: gate}); err == nil {
		t.Fatal("NewWithSink accepted a nil gated sink")
	}
}

// equalStrings reports whether two string slices hold the same items in order.
func equalStrings(got, want []string) bool {
	//: different lengths differ.
	if len(got) != len(want) {
		return false
	}
	//: item by item.
	for index := range got {
		//: the first difference decides.
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
