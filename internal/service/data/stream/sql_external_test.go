package stream_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// sqlDialects are the three engines every SQL case runs on.
var sqlDialects = []coresql.Dialect{coresql.DialectPostgres, coresql.DialectMySQL, coresql.DialectSQLite}

// eachDialect runs fn once per dialect, in parallel, over a fresh fixture.
func eachDialect(
	t *testing.T, policy corestream.PolicyValue, fn func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock),
) {
	t.Helper()
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			fn(t, newSQLFixture(t, dialect, clk, policy), clk)
		})
	}
}

// inTransaction runs fn inside a transaction of fx's transactor and returns
// what the transaction returned.
func inTransaction(t *testing.T, fx *sqlFixture, fn func(ctx context.Context) error) error {
	t.Helper()
	return fx.tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		return fn(ctx)
	})
}

// isClosed reports whether signal has been closed.
func isClosed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// TestSQLAnAppendExistsIfAndOnlyIfItsTransactionCommits is the reason the SQL
// engine exists (ADR 0151): appended inside the caller's transaction, the
// records are invisible to every reader until that transaction commits, a
// rollback leaves none and moves no offset, and the readers are woken by the
// commit and not before.
func TestSQLAnAppendExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		signal := sibling[corestream.Waker](t, fx.stream).Wake()
		errRolledBack := errors.New("the business write failed")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, appendErr := fx.stream.Append(ctx, []corestream.RecordValue{record("rolled back")}); appendErr != nil {
				t.Fatalf("Append() inside the transaction = %v", appendErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's own error", err)
		}
		if fx.engine.rows() != 0 || boundsOK(t, fx.stream, 0) != (corestream.BoundsValue{}) {
			t.Fatalf("a rolled-back Append left %d rows, bounds %+v", fx.engine.rows(), boundsOK(t, fx.stream, 0))
		}
		if isClosed(signal) {
			t.Fatal("a rolled-back Append closed the wake signal")
		}
		err = inTransaction(t, fx, func(ctx context.Context) error {
			got, appendErr := fx.stream.Append(ctx, []corestream.RecordValue{record("one"), record("two")})
			if appendErr != nil {
				return appendErr
			}
			if got.First != 0 || got.Last != 1 {
				t.Errorf("Append() = %+v, want offsets 0..1: the rollback took its offset back", got)
			}
			if inside := readOK(t, fx.stream, 0, 0, 10); len(inside) != 0 {
				t.Errorf("a reader outside the transaction saw %d records before the commit", len(inside))
			}
			if isClosed(signal) {
				t.Error("an Append whose transaction has not committed closed the wake signal")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		if !isClosed(signal) {
			t.Fatal("the commit of an Append did not close the wake signal")
		}
		if entries := readOK(t, fx.stream, 0, 0, 10); len(entries) != 2 || string(entries[1].Record.Payload) != "two" {
			t.Fatalf("Read() after the commit = %+v, want the two records", entries)
		}
	})
}

// TestSQLAFailedAppendLeavesTheCallersTransactionUsable pins the savepoint
// around a joined Append: a statement the database fails — on PostgreSQL one
// that leaves the transaction aborted — is undone alone, and a caller that
// catches it appends again and commits.
func TestSQLAFailedAppendLeavesTheCallersTransactionUsable(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		fx.engine.failNext("insertRows", errors.New("payload SECRET-BYTES refused"))
		err := inTransaction(t, fx, func(ctx context.Context) error {
			_, failed := fx.stream.Append(ctx, []corestream.RecordValue{record("failed")})
			if !errs.HasCode(failed, corestream.CodeStreamBackendFailed) {
				t.Fatalf("the failed Append = %v, want STREAM_BACKEND_FAILED", failed)
			}
			if strings.Contains(failed.Error(), "SECRET") {
				t.Fatalf("the refusal %q renders the driver's words", failed.Error())
			}
			_, appendErr := fx.stream.Append(ctx, []corestream.RecordValue{record("kept")})
			return appendErr
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil — the caller caught the failure", err)
		}
		entries := readOK(t, fx.stream, 0, 0, 10)
		if len(entries) != 1 || entries[0].Offset != 0 || string(entries[0].Record.Payload) != "kept" {
			t.Fatalf("Read() = %+v, want the kept record alone at offset 0", entries)
		}
	})
}

// TestSQLOffsetsCommitInOrderUnderConcurrentAppenders: appenders in
// transactions of their own, some of which roll back, race on one partition
// while a reader follows it. Every committed record holds an offset, every
// offset a committed record — no gap where a rollback was — and the reader,
// which reads from where it stopped, never finds a hole it would have
// skipped.
func TestSQLOffsetsCommitInOrderUnderConcurrentAppenders(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		const writers, each = 8, 25
		errRollback := errors.New("rolled back on purpose")
		stop := make(chan struct{})
		seen := make(chan int, 1)
		go func() {
			var next corestream.Offset
			defer func() { seen <- int(next) }()
			for {
				//: a read begun after the stop sees every commit.
				stopped := isClosed(stop)
				entries, err := fx.stream.Read(context.Background(), 0, next, 64)
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
					err := inTransaction(t, fx, func(ctx context.Context) error {
						if _, err := fx.stream.Append(ctx, []corestream.RecordValue{record("r"), record("r")}); err != nil {
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
		if got := <-seen; got != committed {
			t.Fatalf("the reader followed %d records, want the %d committed", got, committed)
		}
		if bounds := boundsOK(t, fx.stream, 0); bounds != (corestream.BoundsValue{Next: corestream.Offset(committed)}) {
			t.Fatalf("Bounds() = %+v, want {0 %d}: a rollback left a gap", bounds, committed)
		}
	})
}

// TestSQLStatementsPerCall pins the round trips: an Append is the head's lock,
// its read, one INSERT per chunk of SQLRowsPerStatement records and the head's
// advance; a Read is one statement whatever it returns; Bounds and Committed
// one; Commit two, the head's next offset and the cursor's upsert.
func TestSQLStatementsPerCall(t *testing.T) {
	t.Parallel()
	chunk := svcstream.SQLRowsPerStatementForTest
	eachDialect(t, corestream.PolicyValue{}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		committer := sibling[corestream.Committer](t, fx.stream)
		for _, n := range []int{1, chunk, chunk + 1, 2*chunk + 7} {
			batch := make([]corestream.RecordValue, n)
			for i := range batch {
				batch[i] = record("x")
			}
			fx.engine.resetLog()
			appendOK(t, fx.stream, batch...)
			chunks := (n + chunk - 1) / chunk
			want := append([]string{"lockHead", "head"}, repeat("insertRows", chunks)...)
			want = append(want, "advance")
			assertStatements(t, fx.engine, "Append of "+strconv.Itoa(n), want)
		}
		end := boundsOK(t, fx.stream, 0).Next
		calls := []struct {
			name string
			call func() error
			want []string
		}{
			{name: "Read", want: []string{"read"}, call: func() error { _, err := fx.stream.Read(t.Context(), 0, 3, 100); return err }},
			{name: "Read at the end", want: []string{"read"}, call: func() error {
				_, err := fx.stream.Read(t.Context(), 0, end, 1)
				return err
			}},
			{name: "Bounds", want: []string{"bounds"}, call: func() error { _, err := fx.stream.Bounds(t.Context(), 0); return err }},
			{name: "Commit", want: []string{"headNext", "commit"}, call: func() error { return committer.Commit(t.Context(), "g", 0, 5) }},
			{name: "Committed", want: []string{"committed"}, call: func() error { _, err := committer.Committed(t.Context(), "g", 0); return err }},
		}
		for _, c := range calls {
			fx.engine.resetLog()
			if err := c.call(); err != nil {
				t.Fatalf("%s = %v", c.name, err)
			}
			assertStatements(t, fx.engine, c.name, c.want)
		}
	})
}

// repeat returns n copies of role.
func repeat(role string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = role
	}
	return out
}

// assertStatements fails when engine logged other statements than want.
func assertStatements(t *testing.T, engine *sqlEngine, call string, want []string) {
	t.Helper()
	got := engine.statements()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s sent %v, want %v", call, got, want)
	}
}

// TestSQLACommitInTheHandlersTransactionCommitsWithIt is exactly-once in
// effect: a Commit made on a handler's transaction's context is undone by
// that transaction's rollback and kept by its commit.
func TestSQLACommitInTheHandlersTransactionCommitsWithIt(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{}, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		committer := sibling[corestream.Committer](t, fx.stream)
		appendOK(t, fx.stream, record("a"), record("b"))
		errHandler := errors.New("the handler's effect failed")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if commitErr := committer.Commit(ctx, "billing", 0, 2); commitErr != nil {
				t.Fatalf("Commit() inside the transaction = %v", commitErr)
			}
			return errHandler
		})
		if !errors.Is(err, errHandler) {
			t.Fatalf("Transact() = %v, want the handler's error", err)
		}
		if _, committedErr := committer.Committed(t.Context(), "billing", 0); !errs.HasCode(committedErr, corestream.CodeUnknownGroup) {
			t.Fatalf("Committed() after the rollback = %v, want UNKNOWN_GROUP", committedErr)
		}
		err = inTransaction(t, fx, func(ctx context.Context) error {
			return committer.Commit(ctx, "billing", 0, 2)
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		if got, committedErr := committer.Committed(t.Context(), "billing", 0); committedErr != nil || got != 2 {
			t.Fatalf("Committed() after the commit = %d, %v, want 2", got, committedErr)
		}
	})
}

// TestSQLTrimAndAgeReleaseTheRowsBelowTheFirstOffset: the storage follows the
// first offset — a Trim deletes the rows below it, and an Append past the
// sweep period deletes the rows past RetainFor.
func TestSQLTrimAndAgeReleaseTheRowsBelowTheFirstOffset(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{RetainFor: time.Hour}, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		trimmer := sibling[corestream.Trimmer](t, fx.stream)
		batch := make([]corestream.RecordValue, 3000)
		for i := range batch {
			batch[i] = record("x")
		}
		appendOK(t, fx.stream, batch...)
		if err := trimmer.Trim(t.Context(), 0, 2500); err != nil {
			t.Fatalf("Trim(2500) = %v", err)
		}
		if fx.engine.rows() != 500 {
			t.Fatalf("Trim(2500) left %d rows, want 500", fx.engine.rows())
		}
		clk.Advance(2 * time.Hour)
		appendOK(t, fx.stream, record("young"))
		if fx.engine.rows() != 1 {
			t.Fatalf("the sweep left %d rows, want the young one", fx.engine.rows())
		}
		if bounds := boundsOK(t, fx.stream, 0); bounds != (corestream.BoundsValue{First: 3000, Next: 3001}) {
			t.Fatalf("Bounds() = %+v, want {3000 3001}", bounds)
		}
	})
}

// TestSQLRefusesWhatItCannotRun: every configuration no SQL stream could run
// is refused at construction, and SQLMigration refuses the same names.
func TestSQLRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	tm := sqlTransactor(t, newSQLEngine(coresql.DialectSQLite, eventsTable), coresql.DialectSQLite)
	valid := svcstream.SQLConfig{Transactor: tm, Dialect: coresql.DialectSQLite, Table: eventsTable}
	cases := []struct {
		name   string
		change func(*svcstream.SQLConfig)
		code   errs.Code
	}{
		{name: "no transactor", change: func(c *svcstream.SQLConfig) { c.Transactor = nil }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a transactor that cannot join", change: func(c *svcstream.SQLConfig) { c.Transactor = bareTransactor{} }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "no dialect", change: func(c *svcstream.SQLConfig) { c.Dialect = coresql.DialectUnknown }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "an upper-case table", change: func(c *svcstream.SQLConfig) { c.Table = "Events" }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a quoted table", change: func(c *svcstream.SQLConfig) { c.Table = `x"; DROP TABLE y; --` }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a double underscore", change: func(c *svcstream.SQLConfig) { c.Table = "a__heads" }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a trailing underscore", change: func(c *svcstream.SQLConfig) { c.Table = "events_" }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a table too long", change: func(c *svcstream.SQLConfig) { c.Table = strings.Repeat("a", 55) }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "SQLite's prefix", change: func(c *svcstream.SQLConfig) { c.Table = "sqlite_events" }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a byte retention", change: func(c *svcstream.SQLConfig) { c.Policy.RetainBytes = 1 }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a byte ceiling", change: func(c *svcstream.SQLConfig) { c.Policy.MaxBytes = 1 << 20 }, code: corestream.CodeSQLStreamMisconfigured},
		{name: "a negative policy", change: func(c *svcstream.SQLConfig) { c.Policy.Partitions = -1 }, code: corestream.CodeStreamMisconfigured},
	}
	runCase := func(t *testing.T, c struct {
		name   string
		change func(*svcstream.SQLConfig)
		code   errs.Code
	},
	) {
		cfg := valid
		c.change(&cfg)
		if _, err := svcstream.NewSQL(cfg); !errs.HasCode(err, c.code) {
			t.Fatalf("NewSQL(%s) = %v, want code %v", c.name, err, c.code)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
	if _, err := svcstream.NewSQL(valid); err != nil {
		t.Fatalf("NewSQL(valid) = %v, want nil", err)
	}
	if _, err := svcstream.SQLMigration(coresql.DialectSQLite, "a__b", 1); !errs.HasCode(err, corestream.CodeSQLStreamMisconfigured) {
		t.Fatalf("SQLMigration(a__b) = %v, want SQL_STREAM_MISCONFIGURED", err)
	}
	if _, err := svcstream.SQLMigration(coresql.DialectUnknown, eventsTable, 1); !errs.HasCode(err, corestream.CodeSQLStreamMisconfigured) {
		t.Fatalf("SQLMigration(no dialect) = %v, want SQL_STREAM_MISCONFIGURED", err)
	}
}

// bareTransactor is a Transactor that is neither a Joiner nor a Deferrer.
type bareTransactor struct{}

// Transact runs nothing.
func (bareTransactor) Transact(context.Context, coresql.TxOptionsValue, coresql.TxFunc) error {
	return nil
}

// TestSQLTheStatementsAsSent pins the statements whose grammar differs per
// engine, spelled out by hand: a test that rebuilt them with the renderer
// would keep exactly the bug it should catch.
func TestSQLTheStatementsAsSent(t *testing.T) {
	t.Parallel()
	pinned := []struct {
		dialect    coresql.Dialect
		role, text string
	}{
		{coresql.DialectPostgres, "lockHead", `INSERT INTO "app_events__heads" (part, next_offset, first_offset, last_at) VALUES ($1, 0, 0, 0)` +
			` ON CONFLICT (part) DO UPDATE SET next_offset = "app_events__heads".next_offset`},
		{coresql.DialectMySQL, "lockHead", "INSERT INTO `app_events__heads` (part, next_offset, first_offset, last_at) VALUES (?, 0, 0, 0)" +
			" ON DUPLICATE KEY UPDATE next_offset = next_offset"},
		{coresql.DialectSQLite, "lockHead", `INSERT INTO "app_events__heads" (part, next_offset, first_offset, last_at) VALUES (?, 0, 0, 0)` +
			` ON CONFLICT (part) DO UPDATE SET next_offset = next_offset`},
		{coresql.DialectPostgres, "head", `SELECT next_offset, first_offset, last_at FROM "app_events__heads" WHERE part = $1 FOR UPDATE`},
		{coresql.DialectSQLite, "head", `SELECT next_offset, first_offset, last_at FROM "app_events__heads" WHERE part = ?`},
		{coresql.DialectPostgres, "read", `SELECT h.next_offset, r.record_offset, r.appended_at, r.record_key, r.payload FROM "app_events__heads" h` +
			` LEFT JOIN "app_events" r ON r.part = h.part AND r.record_offset >= h.first_offset AND r.appended_at > $1 AND r.record_offset >= $2` +
			` WHERE h.part = $3 ORDER BY r.record_offset LIMIT $4`},
		{coresql.DialectPostgres, "commit", `INSERT INTO "app_events__cursors" (grp, part, next_offset, grp_name) VALUES ($1, $2, $3, $4)` +
			` ON CONFLICT (grp, part) DO UPDATE SET next_offset = $5`},
		{coresql.DialectMySQL, "commit", "INSERT INTO `app_events__cursors` (grp, part, next_offset, grp_name) VALUES (?, ?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE next_offset = ?"},
	}
	for _, want := range pinned {
		rendered := svcstream.RenderSQLForTest(want.dialect, eventsTable)
		if rendered.Fixed[want.text] != want.role {
			t.Errorf("%s %s is not rendered as pinned:\n%s", want.dialect, want.role, want.text)
		}
	}
	if got := svcstream.RenderSQLForTest(coresql.DialectPostgres, eventsTable).InsertRows(2); got != `INSERT INTO "app_events"`+
		` (part, record_offset, appended_at, record_key, payload) VALUES ($1, $2, $3, $4, $5), ($6, $7, $8, $9, $10)` {
		t.Errorf("InsertRows(2) = %s", got)
	}
	migration, err := svcstream.SQLMigration(coresql.DialectPostgres, eventsTable, 7)
	if err != nil || migration.Version != 7 || migration.Name != "stream app_events" || migration.Checksum == "" {
		t.Fatalf("SQLMigration() = %+v, %v", migration, err)
	}
	if create := svcstream.RenderSQLForTest(coresql.DialectPostgres, eventsTable).Create; len(create) != 3 || create[0] !=
		`CREATE TABLE IF NOT EXISTS "app_events" (part integer NOT NULL, record_offset bigint NOT NULL, appended_at bigint NOT NULL,`+
			` record_key bytea NULL, payload bytea NULL, PRIMARY KEY (part, record_offset), UNIQUE (part, appended_at, record_offset))` {
		t.Errorf("the records' DDL = %v", create)
	}
}
