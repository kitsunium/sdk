package levelgate

import (
	"context"
	"errors"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
)

// batchRecorder records every call it receives: the single Writes and the
// payloads of each WriteBatch; failAfter, when positive, fails the call that
// would deliver the record past it.
type batchRecorder struct {
	writes    []string
	batches   [][]string
	delivered int
	failAfter int
}

var errRecorder = errors.New("recorder full")

func (b *batchRecorder) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	if b.failAfter > 0 && b.delivered == b.failAfter {
		return 0, errRecorder
	}
	b.delivered++
	b.writes = append(b.writes, string(p))
	return len(p), nil
}

func (b *batchRecorder) WriteBatch(_ context.Context, _ []corelogger.RecordEvent, payloads [][]byte) (int, error) {
	run := make([]string, 0, len(payloads))
	for _, p := range payloads {
		if b.failAfter > 0 && b.delivered == b.failAfter {
			b.batches = append(b.batches, run)
			return len(run), errRecorder
		}
		b.delivered++
		run = append(run, string(p))
	}
	b.batches = append(b.batches, run)
	return len(run), nil
}

func (*batchRecorder) Flush(context.Context) error { return nil }
func (*batchRecorder) Close() error                { return nil }

// plainRecorder hides WriteBatch, so the gate must write record by record.
type plainRecorder struct{ inner *batchRecorder }

func (p plainRecorder) Write(ctx context.Context, r corelogger.RecordEvent, b []byte) (int, error) {
	return p.inner.Write(ctx, r, b)
}
func (plainRecorder) Flush(context.Context) error { return nil }
func (plainRecorder) Close() error                { return nil }

// gateBatch is a five-record batch whose 2nd and 4th records are below Warn.
func gateBatch() ([]corelogger.RecordEvent, [][]byte) {
	lv := []level.Level{level.Error, level.Debug, level.Warn, level.Info, level.Error}
	recs := make([]corelogger.RecordEvent, len(lv))
	payloads := make([][]byte, len(lv))
	for i, l := range lv {
		recs[i] = corelogger.RecordEvent{Level: l}
		payloads[i] = []byte{byte('a' + i)}
	}
	return recs, payloads
}

// TestGateWriteBatchForwardsRunsOfKeptRecords (#807): the kept records reach a
// BatchSink in runs cut at each dropped one, in order, and the batch reports
// every record delivered.
func TestGateWriteBatchForwardsRunsOfKeptRecords(t *testing.T) {
	t.Parallel()
	inner := &batchRecorder{}
	g := Floor(inner, level.Warn).(corelogger.BatchSink)
	recs, payloads := gateBatch()
	written, err := g.WriteBatch(t.Context(), recs, payloads)
	if err != nil || written != 5 {
		t.Fatalf("WriteBatch = %d, %v; want 5, nil", written, err)
	}
	want := [][]string{{"a"}, {"c"}, {"e"}}
	if len(inner.batches) != len(want) {
		t.Fatalf("batches %v, want %v", inner.batches, want)
	}
	for i := range want {
		if len(inner.batches[i]) != 1 || inner.batches[i][0] != want[i][0] {
			t.Fatalf("batches %v, want %v", inner.batches, want)
		}
	}
}

// TestGateWriteBatchWritesRecordByRecordToAPlainSink: an inner sink without
// the sibling port receives one Write per kept record.
func TestGateWriteBatchWritesRecordByRecordToAPlainSink(t *testing.T) {
	t.Parallel()
	inner := &batchRecorder{}
	g := Floor(plainRecorder{inner}, level.Warn).(corelogger.BatchSink)
	recs, payloads := gateBatch()
	if written, err := g.WriteBatch(t.Context(), recs, payloads); err != nil || written != 5 {
		t.Fatalf("WriteBatch = %d, %v", written, err)
	}
	if got := inner.writes; len(got) != 3 || got[0] != "a" || got[1] != "c" || got[2] != "e" {
		t.Fatalf("writes %v, want [a c e]", got)
	}
}

// TestGateWriteBatchCountsDeliveredUpToTheFailure: when the inner sink fails
// on the third kept record, the batch reports the four records before it —
// two delivered, one dropped by the floor, one delivered — as written.
func TestGateWriteBatchCountsDeliveredUpToTheFailure(t *testing.T) {
	t.Parallel()
	inner := &batchRecorder{failAfter: 2}
	g := Floor(inner, level.Warn).(corelogger.BatchSink)
	recs, payloads := gateBatch()
	written, err := g.WriteBatch(t.Context(), recs, payloads)
	if !errors.Is(err, errRecorder) || written != 4 {
		t.Fatalf("WriteBatch = %d, %v; want 4, the inner failure", written, err)
	}
}

// TestNewLeveledFollowsTheVar (#1015): the gate reads a live floor per record.
func TestNewLeveledFollowsTheVar(t *testing.T) {
	t.Parallel()
	inner := &batchRecorder{}
	floor := level.NewVar(level.Error)
	g := NewLeveled(inner, floor)
	if _, err := g.Write(t.Context(), corelogger.RecordEvent{Level: level.Warn}, []byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	floor.Set(level.Warn)
	if _, err := g.Write(t.Context(), corelogger.RecordEvent{Level: level.Warn}, []byte("b")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(inner.writes) != 1 || inner.writes[0] != "b" {
		t.Fatalf("writes %v, want [b]", inner.writes)
	}
	if NewLeveled(nil, floor) != nil {
		t.Fatal("a gate over nil is not nil")
	}
	if g := NewLeveled(inner, nil); g == nil {
		t.Fatal("a nil floor did not default")
	}
}
