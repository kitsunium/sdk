//go:build integration

// Package sql_test — the stream's SQL engine (ADR 0231, kitsunium/sdk#822) on
// each real engine: every statement it sends parsed and answered, a record's
// bytes as appended, an Append joined to the caller's transaction and rolled
// back, a group's offset committed in a handler's own transaction, and
// concurrent appenders — some rolling back — whose offsets commit in order
// with no gap a following reader could skip: the head row's lock on
// PostgreSQL and MySQL, the one write lock on SQLite.
//
// The stream has no pkg/v1 facade yet (kitsunium/sdk#825), so the suite
// reaches its engine at internal/service/data/stream, which the e2e module
// may import: it is a module of the repository.
package sql_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// streamEpoch anchors the engines' ManualClocks: every instant the SQL engine
// writes is its own clock's, never the database's NOW().
var streamEpoch = time.Date(2026, time.November, 12, 9, 0, 0, 0, time.UTC)

// streamFixture is one case's SQL stream, its transactor and its clock.
type streamFixture struct {
	tm     sql.Transactor
	stream corestream.Stream
	clk    *clock.ManualClock
}

// openStream migrates three fresh tables on e and opens a stream over them.
func openStream(tb testing.TB, e *engine, policy corestream.PolicyValue) *streamFixture {
	tb.Helper()
	table := uniqueName("events_")
	migration, err := svcstream.SQLMigration(e.dialect, table, 1)
	must(tb, err)
	runner, err := sql.NewMigrator(sql.Config{DB: e.db, Dialect: e.dialect, Pool: sql.PoolConfig{MaxOpen: 16}},
		sql.MigrateConfig{Migrations: []sql.Migration{migration}, VersionTable: uniqueName("versions_")})
	must(tb, err)
	must(tb, runner.Up(tb.Context()))
	tm := transactor(tb, e)
	clk := clock.NewManualClock(streamEpoch)
	s, err := svcstream.NewSQL(svcstream.SQLConfig{Transactor: tm, Dialect: e.dialect, Table: table, Policy: policy, Clock: clk})
	must(tb, err)
	return &streamFixture{tm: tm, stream: s, clk: clk}
}

// TestTheSQLStreamOnEveryEngine runs every statement once: keyed and unkeyed
// batches, the bytes read back, an empty payload, Bounds, OffsetAt, Trim and
// the refusals on either side of the retained range, a group's cursor.
func TestTheSQLStreamOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openStream(t, e, corestream.PolicyValue{Partitions: 2})
		key := []byte{0x00, 0xff, 'k'}
		partition := corestream.PartitionOf(key, 2)
		payload := []byte{0x00, 0x01, 0xfe, 0xff, '\'', '"'}
		first, err := fx.stream.Append(t.Context(), []corestream.RecordValue{{Key: key, Payload: payload}, {Key: key}})
		must(t, err)
		fx.clk.Advance(time.Minute)
		batch := make([]corestream.RecordValue, 450)
		for i := range batch {
			batch[i] = corestream.RecordValue{Key: key, Payload: []byte{byte(i)}}
		}
		second, err := fx.stream.Append(t.Context(), batch)
		must(t, err)
		if first.Partition != partition || first.First != 0 || second.First != 2 || second.Last != 451 {
			t.Fatalf("Append() = %+v then %+v", first, second)
		}
		entries, err := fx.stream.Read(t.Context(), partition, 0, 3)
		must(t, err)
		if len(entries) != 3 || string(entries[0].Record.Payload) != string(payload) || string(entries[0].Record.Key) != string(key) ||
			entries[1].Record.Payload != nil || !entries[2].AppendedAt.Equal(streamEpoch.Add(time.Minute)) {
			t.Fatalf("Read() = %+v", entries)
		}
		at, err := fx.stream.(corestream.Seeker).OffsetAt(t.Context(), partition, streamEpoch.Add(time.Second))
		if err != nil || at != 2 {
			t.Fatalf("OffsetAt() = %d, %v, want 2", at, err)
		}
		must(t, fx.stream.(corestream.Trimmer).Trim(t.Context(), partition, 100))
		bounds, err := fx.stream.Bounds(t.Context(), partition)
		if err != nil || bounds != (corestream.BoundsValue{First: 100, Next: 452}) {
			t.Fatalf("Bounds() = %+v, %v", bounds, err)
		}
		if _, err := fx.stream.Read(t.Context(), partition, 99, 1); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
			t.Fatalf("Read(99) = %v, want OFFSET_TRIMMED", err)
		}
		if _, err := fx.stream.Read(t.Context(), partition, 453, 1); !errs.HasCode(err, corestream.CodeOffsetAhead) {
			t.Fatalf("Read(453) = %v, want OFFSET_AHEAD", err)
		}
		if got, err := fx.stream.Read(t.Context(), 1-partition, 0, 1); err != nil || len(got) != 0 {
			t.Fatalf("Read(the other partition) = %+v, %v, want nothing", got, err)
		}
		committer := fx.stream.(corestream.Committer)
		if _, err := committer.Committed(t.Context(), "billing", partition); !errs.HasCode(err, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed(new group) = %v, want UNKNOWN_GROUP", err)
		}
		for _, next := range []corestream.Offset{200, 120, 120} {
			must(t, committer.Commit(t.Context(), "billing", partition, next))
			if got, err := committer.Committed(t.Context(), "billing", partition); err != nil || got != next {
				t.Fatalf("Committed() = %d, %v, want %d", got, err, next)
			}
		}
	})
}

// TestAnAppendJoinsTheCallersTransactionOnEveryEngine: the records exist if
// and only if the caller's transaction commits, and the wake fires on the
// commit.
func TestAnAppendJoinsTheCallersTransactionOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openStream(t, e, corestream.PolicyValue{})
		signal := fx.stream.(corestream.Waker).Wake()
		errRolledBack := errors.New("the business write failed")
		err := sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			_, appendErr := fx.stream.Append(ctx, []corestream.RecordValue{{Payload: []byte("rolled back")}})
			must(t, appendErr)
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's error", err)
		}
		if bounds, err := fx.stream.Bounds(t.Context(), 0); err != nil || bounds.Next != 0 {
			t.Fatalf("Bounds() after the rollback = %+v, %v", bounds, err)
		}
		must(t, sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			_, appendErr := fx.stream.Append(ctx, []corestream.RecordValue{{Payload: []byte("committed")}})
			return appendErr
		}))
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatal("the commit did not close the wake signal")
		}
		entries, err := fx.stream.Read(t.Context(), 0, 0, 10)
		if err != nil || len(entries) != 1 || entries[0].Offset != 0 || string(entries[0].Record.Payload) != "committed" {
			t.Fatalf("Read() = %+v, %v, want the committed record at offset 0", entries, err)
		}
	})
}

// TestACommitInTheHandlersTransactionOnEveryEngine is exactly-once in effect:
// the group's offset, committed on the handler's transaction's context, is
// undone by its rollback and kept by its commit.
func TestACommitInTheHandlersTransactionOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openStream(t, e, corestream.PolicyValue{})
		committer := fx.stream.(corestream.Committer)
		_, err := fx.stream.Append(t.Context(), []corestream.RecordValue{{Payload: []byte("a")}, {Payload: []byte("b")}})
		must(t, err)
		errHandler := errors.New("the handler's effect failed")
		err = sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			must(t, committer.Commit(ctx, "billing", 0, 2))
			return errHandler
		})
		if !errors.Is(err, errHandler) {
			t.Fatalf("Transact() = %v, want the handler's error", err)
		}
		if _, err := committer.Committed(t.Context(), "billing", 0); !errs.HasCode(err, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed() after the rollback = %v, want UNKNOWN_GROUP", err)
		}
		must(t, sql.Transact(t.Context(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
			return committer.Commit(ctx, "billing", 0, 2)
		}))
		if got, err := committer.Committed(t.Context(), "billing", 0); err != nil || got != 2 {
			t.Fatalf("Committed() after the commit = %d, %v, want 2", got, err)
		}
	})
}

// TestConcurrentAppendersCommitInOrderOnEveryEngine: sixteen appenders in
// transactions of their own race on one partition, one in five rolling back,
// while a reader follows it from where it stopped. The reader never meets an
// offset past a hole, and at the end the partition holds every committed
// record at consecutive offsets.
func TestConcurrentAppendersCommitInOrderOnEveryEngine(t *testing.T) {
	t.Parallel()
	eachEngine(t, func(t *testing.T, e *engine) {
		fx := openStream(t, e, corestream.PolicyValue{})
		const writers, each = 16, 10
		errRollback := errors.New("rolled back on purpose")
		stop := make(chan struct{})
		followed := make(chan int, 1)
		go func() {
			var next corestream.Offset
			defer func() { followed <- int(next) }()
			for {
				stopped := false
				select {
				case <-stop:
					stopped = true
				default:
				}
				entries, err := fx.stream.Read(context.Background(), 0, next, 100)
				if err != nil {
					t.Errorf("Read(%d) = %v", next, err)
					return
				}
				for _, entry := range entries {
					if entry.Offset != next {
						t.Errorf("the reader at %d was handed offset %d", next, entry.Offset)
						return
					}
					next++
				}
				if stopped && len(entries) == 0 {
					return
				}
			}
		}()
		var wg sync.WaitGroup
		var mu sync.Mutex
		committed := 0
		for w := range writers {
			wg.Go(func() {
				for i := range each {
					rollback := (w+i)%5 == 0
					err := sql.Transact(context.Background(), fx.tm, func(ctx context.Context, _ sql.Executor) error {
						if _, err := fx.stream.Append(ctx, []corestream.RecordValue{{Payload: []byte("x")}, {Payload: []byte("y")}}); err != nil {
							return err
						}
						if rollback {
							return errRollback
						}
						return nil
					})
					switch {
					case rollback && errors.Is(err, errRollback):
					case !rollback && err == nil:
						mu.Lock()
						committed += 2
						mu.Unlock()
					default:
						t.Errorf("Transact() = %v, rollback %v", err, rollback)
					}
				}
			})
		}
		wg.Wait()
		close(stop)
		if got := <-followed; got != committed {
			t.Fatalf("the reader followed %d records, want the %d committed", got, committed)
		}
		bounds, err := fx.stream.Bounds(t.Context(), 0)
		if err != nil || bounds != (corestream.BoundsValue{Next: corestream.Offset(committed)}) {
			t.Fatalf("Bounds() = %+v, %v, want {0 %d}: a rollback left a gap", bounds, err, committed)
		}
	})
}
