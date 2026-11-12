package stream_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"io"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// ExampleNewMemory appends two records under one key and reads them back
// from the partition the key hashes to, in the order they were appended.
func ExampleNewMemory() {
	ctx := context.Background()
	s, err := stream.NewMemory(stream.MemoryConfig{Policy: stream.Policy{Partitions: 4}})
	if err != nil {
		return
	}
	key := []byte("order-42")
	appended, err := s.Append(ctx, []stream.Record{
		{Key: key, Payload: []byte("placed")},
		{Key: key, Payload: []byte("paid")},
	})
	if err != nil {
		return
	}
	fmt.Println(appended.Partition == stream.PartitionOf(key, s.Partitions()), appended.First, appended.Last)
	entries, err := s.Read(ctx, appended.Partition, appended.First, 10)
	if err != nil {
		return
	}
	for _, e := range entries {
		fmt.Println(e.Offset, string(e.Record.Payload))
	}
	// Output:
	// true 0 1
	// 0 placed
	// 1 paid
}

// ExampleSubscribe reads a stream on behalf of the group "billing": the
// handler gets the partition's records in offset order, and the group's
// offset is committed past every batch it accepted.
func ExampleSubscribe() {
	s, err := stream.NewMemory(stream.MemoryConfig{Policy: stream.Policy{Partitions: 1}})
	if err != nil {
		return
	}
	for _, payload := range []string{"a", "b", "c"} {
		if _, err := s.Append(context.Background(), []stream.Record{{Payload: []byte(payload)}}); err != nil {
			return
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	seen := 0
	err = stream.Subscribe(ctx, s, stream.SubscriberConfig{
		Group: "billing",
		Handler: func(_ context.Context, entries []stream.Entry) error {
			for _, e := range entries {
				fmt.Println(e.Offset, string(e.Record.Payload))
				seen++
			}
			if seen == 3 {
				stop() // a stop still commits the batch accepted as it lands
			}
			return nil
		},
		HandlerIsIdempotent: true, // required: a crash before the commit delivers again
	})
	fmt.Println("subscribe:", err)
	next, err := s.(stream.Committer).Committed(context.Background(), "billing", 0)
	fmt.Println("committed:", next, err)
	// Output:
	// 0 a
	// 1 b
	// 2 c
	// subscribe: <nil>
	// committed: 3 <nil>
}

// ExampleNewSQL keeps the stream in the caller's database: the Append joins
// the transaction the order is written in, so the record exists if and only
// if the order does, and a handler under CommitByHandler commits its group's
// offset in the transaction that holds its own effect — exactly once in
// effect.
func ExampleNewSQL() {
	ctx := context.Background()
	var db *stdsql.DB // the application's pool, opened with its driver
	tm, err := sql.NewTransactor(sql.Config{DB: db, Dialect: sql.DialectPostgres})
	if err != nil {
		return
	}
	// Run by the application's Migrator, under its own version table.
	_, err = stream.SQLMigration(sql.DialectPostgres, "app_orders", 20261112120000)
	if err != nil {
		return
	}
	orders, err := stream.NewSQL(stream.SQLConfig{Transactor: tm, Dialect: sql.DialectPostgres, Table: "app_orders"})
	if err != nil {
		return
	}
	err = tm.Transact(ctx, sql.TxOptions{}, func(ctx context.Context, ex sql.Executor) error {
		if _, err := ex.ExecContext(ctx, "INSERT INTO orders (id) VALUES ($1)", "42"); err != nil {
			return err // no record either
		}
		_, err := orders.Append(ctx, []stream.Record{{Key: []byte("42"), Payload: []byte("placed")}})
		return err // joins this transaction
	})
	if err != nil {
		return
	}
	_ = stream.Subscribe(ctx, orders, stream.SubscriberConfig{
		Group:               "invoices",
		Commit:              stream.CommitByHandler,
		HandlerIsIdempotent: true, // the effect and the offset are one transaction
		Handler: func(ctx context.Context, entries []stream.Entry) error {
			return tm.Transact(ctx, sql.TxOptions{}, func(ctx context.Context, ex sql.Executor) error {
				if _, err := ex.ExecContext(ctx, "INSERT INTO invoices (order_id) VALUES ($1)", string(entries[0].Record.Key)); err != nil {
					return err
				}
				last := entries[len(entries)-1]
				return orders.(stream.Committer).Commit(ctx, "invoices", last.Partition, last.Offset+1)
			})
		},
	})
}

// spyTransactor is a transactor of the caller's making with the two siblings
// NewSQL asks for. NewSQL sends no statement, so it is never called.
type spyTransactor struct{}

// Transact runs fn with no transaction at all.
func (spyTransactor) Transact(ctx context.Context, _ sql.TxOptions, fn sql.TxFunc) error {
	return fn(ctx, nil)
}

// Join answers no executor and no transaction.
func (spyTransactor) Join(context.Context) (sql.Executor, bool) { return nil, false }

// Defer holds nothing.
func (spyTransactor) Defer(context.Context, func()) bool { return false }

// facadeStream builds the engine name names through the public surface only.
func facadeStream(t *testing.T, name string) stream.Stream {
	t.Helper()
	var (
		s   stream.Stream
		err error
	)
	switch name {
	case "memory":
		s, err = stream.NewMemory(stream.MemoryConfig{Policy: stream.Policy{Partitions: 2}})
	case "file":
		requireFileEngine(t)
		s, err = stream.NewFile(stream.FileConfig{Dir: t.TempDir(), Policy: stream.Policy{Partitions: 2}})
	default:
		s, err = stream.NewSQL(stream.SQLConfig{Transactor: spyTransactor{}, Dialect: sql.DialectPostgres, Table: "app_orders"})
	}
	if err != nil {
		t.Fatalf("New(%s) = %v, want nil", name, err)
	}
	if closer, ok := s.(io.Closer); ok {
		t.Cleanup(func() {
			if cerr := closer.Close(); cerr != nil {
				t.Errorf("Close() = %v, want nil", cerr)
			}
		})
	}
	return s
}

// requireFileEngine skips t where the file engine is refused by design, and
// only there: its cursors live in vfs's disk filesystem, and where vfs.NewOS
// refuses the platform (Windows, illumos, Solaris: ADR 0018) NewFile must
// refuse with the very same error, which is asserted before the skip (ADR
// 0095). The predicate is vfs's refusal, never a list of GOOS.
func requireFileEngine(t *testing.T) {
	t.Helper()
	disk, vfsErr := vfs.NewOS(t.TempDir())
	if vfsErr == nil {
		if closer, ok := disk.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				t.Fatalf("closing the probe filesystem: %v", err)
			}
		}
		return
	}
	s, err := stream.NewFile(stream.FileConfig{Dir: t.TempDir()})
	if !errors.Is(err, vfsErr) || s != nil {
		t.Fatalf("NewFile on %s = (%v, %v), want (nil, %v), vfs's refusal", runtime.GOOS, s, err, vfsErr)
	}
	t.Skipf("the file engine keeps its cursors in vfs's disk filesystem, which %s refuses by design (%v); NewFile refused it the same way, asserted above (ADR 0095)", runtime.GOOS, vfsErr)
}

// carries reports whether s implements the capability T.
func carries[T any](s stream.Stream) bool {
	_, ok := s.(T)
	return ok
}

// TestEveryFacadeEngineCarriesTheFiveSiblings pins the ADR 0039 shape through
// the aliases, with no internal import: the port is frozen at four methods and
// the five siblings are reached by type assertion on every engine.
func TestEveryFacadeEngineCarriesTheFiveSiblings(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file", "sql"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := facadeStream(t, name)
			for sibling, carried := range map[string]bool{
				"Waker":     carries[stream.Waker](s),
				"Seeker":    carries[stream.Seeker](s),
				"Trimmer":   carries[stream.Trimmer](s),
				"Compactor": carries[stream.Compactor](s),
				"Committer": carries[stream.Committer](s),
			} {
				if !carried {
					t.Errorf("the %s engine does not implement stream.%s", name, sibling)
				}
			}
		})
	}
}

// TestTheFacadeAppendsReadsAndBounds walks a producer and a reader through the
// public names on both local engines: a keyed batch, its bounds, a read past
// the next offset refused.
func TestTheFacadeAppendsReadsAndBounds(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"memory", "file"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := facadeStream(t, name)
			ctx := t.Context()
			var appended stream.Appended
			appended, err := s.Append(ctx, []stream.Record{{Key: []byte("k"), Payload: []byte("v")}})
			if err != nil {
				t.Fatalf("Append() = %v, want nil", err)
			}
			var bounds stream.Bounds
			bounds, err = s.Bounds(ctx, appended.Partition)
			if err != nil || bounds.First != 0 || bounds.Next != 1 {
				t.Fatalf("Bounds() = %+v, %v; want [0, 1)", bounds, err)
			}
			if _, err := s.Read(ctx, appended.Partition, stream.Offset(5), 1); !errors.Is(err, stream.OffsetAhead) ||
				!errs.HasCode(err, stream.CodeOffsetAhead) {
				t.Fatalf("Read(past the next offset) = %v, want OffsetAhead", err)
			}
			if _, err := s.Append(ctx, nil); !errors.Is(err, stream.InvalidBatchSize) {
				t.Fatalf("Append(no record) = %v, want InvalidBatchSize", err)
			}
			var compacted stream.Compacted
			compacted, err = s.(stream.Compactor).Compact(ctx, appended.Partition)
			if err != nil || compacted.Dropped != 0 {
				t.Fatalf("Compact() = %+v, %v; want nothing dropped from one record", compacted, err)
			}
		})
	}
}

// TestTheFacadeRefusesWhatEachEngineCannotRun spells the refusals a caller
// meets first, by sentinel and by code, with no internal import.
func TestTheFacadeRefusesWhatEachEngineCannotRun(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	if _, err := stream.NewMemory(stream.MemoryConfig{Policy: stream.Policy{RetainFor: -1}}); !errors.Is(err, stream.StreamMisconfigured) ||
		!errs.HasCode(err, stream.CodeStreamMisconfigured) {
		t.Fatalf("NewMemory(a negative retention) = %v, want StreamMisconfigured", err)
	}
	if _, err := stream.NewSQL(stream.SQLConfig{Transactor: spyTransactor{}, Dialect: sql.DialectPostgres, Table: "App; DROP"}); !errors.Is(err, stream.SQLStreamMisconfigured) ||
		!errs.HasCode(err, stream.CodeSQLStreamMisconfigured) {
		t.Fatalf("NewSQL(a table that is not one) = %v, want SQLStreamMisconfigured", err)
	}
	s := facadeStream(t, "memory")
	err := stream.Subscribe(ctx, s, stream.SubscriberConfig{Group: "g", Handler: func(context.Context, []stream.Entry) error { return nil }})
	if !errors.Is(err, stream.SubscriberMisconfigured) || !errs.HasCode(err, stream.CodeSubscriberMisconfigured) {
		t.Fatalf("Subscribe(no idempotence asserted) = %v, want SubscriberMisconfigured", err)
	}
	if _, err := s.(stream.Committer).Committed(ctx, "never", 0); !errors.Is(err, stream.UnknownGroup) {
		t.Fatalf("Committed(a group that never committed) = %v, want UnknownGroup", err)
	}
	if _, err := s.Append(ctx, []stream.Record{{Key: []byte("a")}, {Key: []byte("b")}}); stream.PartitionOf([]byte("a"), 2) != stream.PartitionOf([]byte("b"), 2) &&
		!errors.Is(err, stream.PartitionsMixed) {
		t.Fatalf("Append(keys on two partitions) = %v, want PartitionsMixed", err)
	}
	if stream.CommitAfterHandler != 0 || stream.DefaultMaxRecordBytes != 1<<20 || stream.DefaultSegmentBytes != 64<<20 {
		t.Fatal("a re-exported constant does not carry the core's value")
	}
}

// TestTheFacadeBuildsTheSQLMigration pins SQLMigration through the public
// names: a migration the caller's Migrator runs and numbers.
func TestTheFacadeBuildsTheSQLMigration(t *testing.T) {
	t.Parallel()
	migration, err := stream.SQLMigration(sql.DialectSQLite, "app_orders", 20261112120000)
	if err != nil {
		t.Fatalf("SQLMigration() = %v, want nil", err)
	}
	if migration.Version != 20261112120000 {
		t.Fatalf("migration.Version = %d, want the caller's number", migration.Version)
	}
}
