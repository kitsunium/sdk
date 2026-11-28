package stream_test

import (
	"context"
	"errors"
	"fmt"
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
// skipped. It runs on the serialising engine and on the concurrent one,
// where only the head's row lock orders the appenders — on MySQL with
// InnoDB's locks on a missing key, on which the head's upsert deadlocked the
// appenders of a fresh partition and rolled a caller's transaction back
// whole (#822).
func TestSQLOffsetsCommitInOrderUnderConcurrentAppenders(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		engine func() *sqlEngine
	}
	var cases []tc
	for _, dialect := range sqlDialects {
		cases = append(cases,
			tc{name: "serialised/" + dialect.String(), engine: func() *sqlEngine { return newSQLEngine(dialect, eventsTable) }},
			tc{name: "rowlocks/" + dialect.String(), engine: func() *sqlEngine { return newConcurrentSQLEngine(dialect, eventsTable, true) }},
		)
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		fx := newSQLFixtureOn(t, c.engine(), clock.NewManualClock(epoch), corestream.PolicyValue{})
		//: every committed record at its offset, in commit order.
		if bad := appendersRace(t, fx); len(bad) > 0 {
			t.Fatalf("the appenders' race went wrong:\n%s", strings.Join(bad, "\n"))
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The ordering test can fail: on a concurrent engine whose locking
// statements lock nothing, the appenders read the same head, and the race
// reports what a real engine without the lock would do — a commit refused on
// a taken offset, or a reader handed a hole. Without this control the test
// above proves nothing: on the serialising engine it cannot fail (#822).
func TestSQLTheOrderingTestFailsOnAnEngineWithoutRowLocks(t *testing.T) {
	t.Parallel()
	type tc struct {
		dialect coresql.Dialect
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		for range 5 {
			fx := newSQLFixtureOn(t, newConcurrentSQLEngine(c.dialect, eventsTable, false), clock.NewManualClock(epoch), corestream.PolicyValue{})
			//: the race caught what an engine without the lock does.
			if bad := appendersRace(t, fx); len(bad) > 0 {
				t.Logf("caught without row locks: %s", bad[0])
				return
			}
		}
		t.Fatal("five races on an engine without row locks found nothing wrong: the ordering test cannot fail")
	}
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) { runCase(t, tc{dialect: dialect}) })
	}
}

// TestSQLAFreshPartitionOutlivesItsFirstAppenderRollingBack forces the state
// the ordering race reaches only when its first appender is one that rolls
// back (#822): the first Append of a partition never written stays
// uncommitted while two more appenders of that partition queue behind it,
// then its transaction rolls back. Both must commit, at offsets 0 and 1. On
// MySQL the head's upsert left the two waiting on the gap of the missing
// key, deadlocked them, and the server rolled one caller's transaction back
// whole: STREAM_BACKEND_FAILED, then SAVEPOINT_FAILED.
func TestSQLAFreshPartitionOutlivesItsFirstAppenderRollingBack(t *testing.T) {
	t.Parallel()
	type tc struct {
		dialect coresql.Dialect
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		const waiters = 2
		engine := newConcurrentSQLEngine(c.dialect, eventsTable, true)
		fx := newSQLFixtureOn(t, engine, clock.NewManualClock(epoch), corestream.PolicyValue{})
		errRollback := errors.New("rolled back on purpose")
		results := make(chan error, waiters)
		first := inTransaction(t, fx, func(ctx context.Context) error {
			//: the partition's first Append, left uncommitted.
			if _, err := fx.stream.Append(ctx, []corestream.RecordValue{record("first")}); err != nil {
				return err
			}
			for range waiters {
				go func() {
					results <- inTransaction(t, fx, func(ctx context.Context) error {
						_, err := fx.stream.Append(ctx, []corestream.RecordValue{record("next")})
						return err
					})
				}()
			}
			//: the rollback comes once both wait on the head.
			if !engine.awaitQueued(0, waiters) {
				t.Errorf("the %d appenders never queued on the head", waiters)
			}
			return errRollback
		})
		//: the first transaction ended as it asked.
		if !errors.Is(first, errRollback) {
			t.Fatalf("the first Transact() = %v, want its own rollback", first)
		}
		for range waiters {
			//: each waiter's transaction commits.
			if err := <-results; err != nil {
				t.Errorf("a waiting appender's Transact() = %v (SAVEPOINT_FAILED in its chain: %v), want nil",
					err, errs.HasCode(err, coresql.CodeSavepointFailed))
			}
		}
		engine.mu.Lock()
		deadlocks := engine.deadlocks
		engine.mu.Unlock()
		//: no caller's transaction rolled back by the engine.
		if deadlocks > 0 {
			t.Errorf("the engine rolled %d transaction(s) back whole on a deadlock over the head row", deadlocks)
		}
		//: the two commits hold offsets 0 and 1, nothing of the rollback.
		if bounds := boundsOK(t, fx.stream, 0); bounds != (corestream.BoundsValue{Next: waiters}) {
			t.Errorf("Bounds() = %+v, want {0 %d}", bounds, waiters)
		}
	}
	for _, dialect := range sqlDialects {
		t.Run(dialect.String(), func(t *testing.T) { runCase(t, tc{dialect: dialect}) })
	}
}

// On MySQL the first Append to a partition inside a caller's transaction
// creates its head on a second connection of the pool (#822): with a pool of
// one, which the caller's transaction holds, it waits for that connection
// until its context ends and fails StreamBackendFailed, leaving the caller's
// transaction usable; once the head exists, the same Append needs no second
// connection.
func TestSQLAMySQLHeadIsCreatedOnASecondConnection(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// known appends outside every transaction first, which creates the
		// head before the caller's transaction opens.
		known bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Parallel()
		engine := newSQLEngine(coresql.DialectMySQL, eventsTable)
		engine.maxOpen = 1
		fx := newSQLFixtureOn(t, engine, clock.NewManualClock(epoch), corestream.PolicyValue{})
		//: the head created on the pool, outside every transaction.
		if c.known {
			appendOK(t, fx.stream, record("outside"))
		}
		var appendErr error
		err := inTransaction(t, fx, func(ctx context.Context) error {
			short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			_, appendErr = fx.stream.Append(short, []corestream.RecordValue{record("inside")})
			return nil
		})
		//: the caller's transaction commits whatever the Append answered.
		if err != nil {
			t.Fatalf("the caller's transaction after the Append: %v", err)
		}
		switch {
		//: no second connection needed.
		case c.known && appendErr != nil:
			t.Fatalf("Append() in a pool of one once the head exists = %v", appendErr)
		//: the second connection waited for until the deadline.
		case !c.known && !errs.HasCode(appendErr, corestream.CodeStreamBackendFailed):
			t.Fatalf("Append() in a pool of one = %v, want STREAM_BACKEND_FAILED at its deadline", appendErr)
		}
	}
	for _, c := range []tc{{name: "missing"}, {name: "known", known: true}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// appendersRace runs the race of the ordering test on fx — eight appenders in
// transactions of their own, one in five rolling back, while a reader
// follows partition 0 from where it stopped — and returns what it found
// wrong, nothing when the offsets committed in order.
func appendersRace(t *testing.T, fx *sqlFixture) []string {
	t.Helper()
	const writers, each = 8, 25
	errRollback := errors.New("rolled back on purpose")
	var mu sync.Mutex
	var bad []string
	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		bad = append(bad, fmt.Sprintf(format, args...))
	}
	stop := make(chan struct{})
	seen := make(chan int, 1)
	go func() {
		var next corestream.Offset
		defer func() { seen <- int(next) }()
		for {
			//: a read begun after the stop sees every commit.
			stopped := isClosed(stop)
			entries, err := fx.stream.Read(context.Background(), 0, next, 64)
			//: the reader stops at a refused read.
			if err != nil {
				report("Read(%d) = %v", next, err)
				return
			}
			for _, entry := range entries {
				//: a hole the reader would skip.
				if entry.Offset != next {
					report("the reader at %d was handed offset %d", next, entry.Offset)
					return
				}
				next++
			}
			//: nothing left after the last commit.
			if stopped && len(entries) == 0 {
				return
			}
		}
	}()
	var wg sync.WaitGroup
	committed := 0
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				rollback := (w+i)%5 == 0
				err := inTransaction(t, fx, func(ctx context.Context) error {
					//: a refused Append fails the caller's transaction.
					if _, err := fx.stream.Append(ctx, []corestream.RecordValue{record("r"), record("r")}); err != nil {
						return err
					}
					//: one in five rolls its Append back.
					if rollback {
						return errRollback
					}
					return nil
				})
				switch {
				//: rolled back as asked.
				case rollback && errors.Is(err, errRollback):
				//: committed.
				case !rollback && err == nil:
					mu.Lock()
					committed += 2
					mu.Unlock()
				//: the caller's transaction did not end as it asked.
				default:
					report("Transact() = %v, rollback %v", err, rollback)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	//: the reader followed every committed record.
	if got := <-seen; got != committed {
		report("the reader followed %d records, want the %d committed", got, committed)
	}
	//: no offset left where a rollback was.
	if bounds, err := fx.stream.Bounds(t.Context(), 0); err != nil || bounds != (corestream.BoundsValue{Next: corestream.Offset(committed)}) {
		report("Bounds() = %+v, %v; want {0 %d}: a rollback left a gap", bounds, err, committed)
	}
	fx.engine.mu.Lock()
	deadlocks := fx.engine.deadlocks
	fx.engine.mu.Unlock()
	//: the cause, when the engine rolled a transaction back on the head.
	if deadlocks > 0 {
		report("the engine rolled %d transaction(s) back whole on a deadlock over the head row", deadlocks)
	}
	return bad
}

// TestSQLStatementsPerCall pins the round trips: an Append is the head's lock,
// its read, one INSERT per chunk of SQLRowsPerStatement records and the head's
// advance — on MySQL the head's creation on the pool the first time a
// partition is written, before the transaction, then its locking read
// (#822); a Read is one statement whatever it returns; Bounds and Committed
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
			want := []string{"lockHead", "head"}
			//: MySQL creates the head on the pool the first time, before the
			//: transaction, then locks it by reading it (#822).
			if fx.engine.dialect == coresql.DialectMySQL {
				want = []string{"head"}
				//: the partition's first Append.
				if n == 1 {
					want = []string{"createHead", "head"}
				}
			}
			want = append(want, repeat("insertRows", chunks)...)
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

// TestSQLCompactDeletesTheRowsItDropsInBoundedStatements: the rows go, in
// DELETEs of at most 256 offsets, a page of 1 024 rows read at a time, and a
// record that ages out before a delete runs stays for the age sweep.
func TestSQLCompactDeletesTheRowsItDropsInBoundedStatements(t *testing.T) {
	t.Parallel()
	eachDialect(t, corestream.PolicyValue{CompactAfter: time.Minute}, func(t *testing.T, fx *sqlFixture, clk *clock.ManualClock) {
		batch := make([]corestream.RecordValue, 1500)
		for i := range batch {
			batch[i] = keyed("k", "v")
		}
		appendOK(t, fx.stream, batch...)
		clk.Advance(time.Minute)
		fx.engine.resetLog()
		got := compactOK(t, fx.stream, 0)
		if got != (corestream.CompactValue{Scanned: 1500, Kept: 1, Dropped: 1499}) {
			t.Fatalf("Compact = %+v, want {1500 1 1499}", got)
		}
		if fx.engine.rows() != 1 {
			t.Fatalf("Compact left %d rows, want 1", fx.engine.rows())
		}
		var scans, deletes int
		for _, role := range fx.engine.statements() {
			switch role {
			case "compactScan":
				scans++
			case "compactDelete":
				deletes++
			}
		}
		if scans != 2 || deletes != 6 {
			t.Fatalf("Compact sent %d scans and %d deletes, want 2 and 6", scans, deletes)
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
		{coresql.DialectMySQL, "head", "SELECT next_offset, first_offset, last_at FROM `app_events__heads` WHERE part = ? FOR UPDATE"},
		{coresql.DialectMySQL, "createHead", "INSERT IGNORE INTO `app_events__heads` (part, next_offset, first_offset, last_at) VALUES (?, 0, 0, 0)"},
		{coresql.DialectSQLite, "head", `SELECT next_offset, first_offset, last_at FROM "app_events__heads" WHERE part = ?`},
		{coresql.DialectPostgres, "read", `SELECT h.next_offset, h.first_offset, (SELECT MAX(x.record_offset) FROM "app_events" x` +
			` WHERE x.part = h.part AND x.record_offset >= h.first_offset AND x.appended_at <= $1), r.record_offset, r.appended_at,` +
			` r.record_key, r.payload FROM "app_events__heads" h LEFT JOIN "app_events" r ON r.part = h.part` +
			` AND r.record_offset >= h.first_offset AND r.appended_at > $2 AND r.record_offset >= $3` +
			` WHERE h.part = $4 ORDER BY r.record_offset LIMIT $5`},
		{coresql.DialectMySQL, "bounds", "SELECT h.next_offset, h.first_offset, (SELECT MAX(x.record_offset) FROM `app_events` x" +
			" WHERE x.part = h.part AND x.record_offset >= h.first_offset AND x.appended_at <= ?) FROM `app_events__heads` h WHERE h.part = ?"},
		{coresql.DialectSQLite, "compactScan", `SELECT record_offset, record_key, appended_at, CASE WHEN payload IS NULL THEN 1 ELSE 0 END` +
			` FROM "app_events" WHERE part = ? AND record_offset >= ? AND appended_at > ? AND appended_at <= ?` +
			` ORDER BY record_offset LIMIT ?`},
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
	if got := svcstream.RenderSQLForTest(coresql.DialectPostgres, eventsTable).CompactDelete(2); got != `DELETE FROM "app_events"`+
		` WHERE part = $1 AND appended_at > $2 AND record_offset IN ($3, $4)` {
		t.Errorf("CompactDelete(2) = %s", got)
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
