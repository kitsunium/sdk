package stream_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// The SQL cases keep the subscribed records on partition 0 and write the
// handler's EFFECT as a record on partition 1 of the same tables, in the
// handler's own transaction: the effect exists if and only if that
// transaction commits, so counting partition 1 counts the effects applied.
const (
	paidPartition   = 0
	effectPartition = 1
)

// effects returns how many effects partition 1 holds.
func effects(t *testing.T, s corestream.Stream) int {
	t.Helper()
	return len(readOK(t, s, effectPartition, 0, 100))
}

// effectRecord is the handler's effect for entry, keyed onto partition 1.
func effectRecord(t *testing.T, entry corestream.EntryValue) corestream.RecordValue {
	t.Helper()
	return keyed(keyOn(t, effectPartition, 2), "effect-of-"+string(entry.Record.Payload))
}

// TestSubscribeCommitByHandlerIsExactlyOnceInEffectOnSQL: the handler writes
// its effect and commits the group's offset in one transaction; the first
// attempt's transaction fails AFTER the write and takes both back, the second
// commits both, and a subscriber stopped right after — a crash, as far as the
// group is concerned — and started again delivers nothing twice: one effect.
func TestSubscribeCommitByHandlerIsExactlyOnceInEffectOnSQL(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		committer := sibling[corestream.Committer](t, fx.stream)
		appendOK(t, fx.stream, keyed(keyOn(t, paidPartition, 2), "pay-1"))
		var attempts atomic.Int64
		errAfterWrite := errors.New("the transaction failed after the handler's write")
		handler := func(ctx context.Context, entries []corestream.EntryValue) error {
			return fx.tm.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, _ coresql.Executor) error {
				for _, entry := range entries {
					if _, err := fx.stream.Append(txCtx, []corestream.RecordValue{effectRecord(t, entry)}); err != nil {
						return err
					}
				}
				last := entries[len(entries)-1]
				if err := committer.Commit(txCtx, "billing", last.Partition, last.Offset+1); err != nil {
					return err
				}
				if attempts.Add(1) == 1 {
					return errAfterWrite
				}
				return nil
			})
		}
		rec := newRecorder(func(entries []corestream.EntryValue) error { return handler(t.Context(), entries) })
		cfg := svcstream.SubscriberConfig{
			Group: "billing", Handler: rec.handle, Partitions: []int{paidPartition},
			Commit: svcstream.CommitByHandler, Retry: quickRetry,
		}
		first := subscribe(t, fx.stream, cfg)
		rec.waitFor(t, "pay-1 accepted", func(ds []delivery) bool { return len(ds) >= 1 })
		if err := first.stop(t); err != nil {
			t.Fatalf("Subscribe() = %v, want nil", err)
		}
		if got := effects(t, fx.stream); got != 1 || attempts.Load() != 2 {
			t.Fatalf("after a failed then a committed attempt: %d effects over %d attempts, want 1 over 2", got, attempts.Load())
		}
		again := newRecorder(func(entries []corestream.EntryValue) error { return handler(t.Context(), entries) })
		cfg.Handler = again.handle
		subscribe(t, fx.stream, cfg)
		appendOK(t, fx.stream, keyed(keyOn(t, paidPartition, 2), "pay-2"))
		again.waitFor(t, "pay-2", func(ds []delivery) bool { return len(ds) >= 1 })
		if got := payloads(again.deliveries()); !slices.Equal(got, []string{"pay-2"}) {
			t.Fatalf("restarted subscriber delivered %v, want only [pay-2]", got)
		}
		if got := effects(t, fx.stream); got != 2 {
			t.Fatalf("effects = %d, want 2: one per record, none twice", got)
		}
	})
}

// TestSubscribeCommitAfterHandlerOnSQLAppliesTheEffectTwiceAfterACrash is the
// contrast: the handler's effect commits in its own transaction, the
// subscriber's commit after it fails, and the group's next subscriber runs
// the handler again — two effects for one record, which is why
// CommitAfterHandler asks for an idempotent handler.
func TestSubscribeCommitAfterHandlerOnSQLAppliesTheEffectTwiceAfterACrash(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		appendOK(t, fx.stream, keyed(keyOn(t, paidPartition, 2), "pay-1"))
		handler := func(_ context.Context, entries []corestream.EntryValue) error {
			return inTransaction(t, fx, func(txCtx context.Context) error {
				_, err := fx.stream.Append(txCtx, []corestream.RecordValue{effectRecord(t, entries[0])})
				return err
			})
		}
		fx.engine.failNext("commit", errors.New("the process died before the commit"))
		cfg := svcstream.SubscriberConfig{Group: "billing", Handler: handler, Partitions: []int{paidPartition}}
		err := subscribe(t, fx.stream, cfg).wait(t)
		if !errs.HasCode(err, corestream.CodeStreamBackendFailed) {
			t.Fatalf("Subscribe() over a failing commit = %v, want STREAM_BACKEND_FAILED", err)
		}
		rec := newRecorder(func(entries []corestream.EntryValue) error { return handler(t.Context(), entries) })
		cfg.Handler = rec.handle
		subscribe(t, fx.stream, cfg)
		rec.waitFor(t, "the redelivery of pay-1", func(ds []delivery) bool { return len(ds) >= 1 })
		if got := effects(t, fx.stream); got != 2 {
			t.Fatalf("effects = %d, want 2: the record was delivered again", got)
		}
	})
}
