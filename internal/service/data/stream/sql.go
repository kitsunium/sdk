package stream

import (
	"context"
	stdsql "database/sql"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// sqlSweepEvery bounds how often an Append releases a partition's expired
// records: at most once per RetainFor, and at least once a minute.
const sqlSweepEvery time.Duration = time.Minute

// sqlStreamKey names one stream for the process's wake table: the database
// pool and the records' table. Two engines over both are one stream.
type sqlStreamKey struct {
	// pool is the executor a context with no transaction runs on.
	pool coresql.Executor
	// table is the records' table.
	table string
}

// sqlWakes is the process's table of SQL streams, so every engine over one
// stream in this process wakes the readers of every other.
var sqlWakes = struct {
	signals map[sqlStreamKey]*wakeSignal
	mu      sync.Mutex
}{signals: map[sqlStreamKey]*wakeSignal{}}

// sqlStream is a stream kept in three tables of the caller's own database.
//
// # Why the caller's database, and what that buys
//
// An Append joins the transaction its context carries (ADR 0151), so its
// records exist if and only if that transaction commits: the outbox, for a
// log. A Commit does the same, so a handler that commits its group's offset
// on its own transaction's context has its effect and its offset in one
// commit — exactly-once in effect.
//
// # Why offsets commit in order
//
// The head row of a partition is locked by the Append's first statement and
// held until its transaction ends, so a second appender of that partition
// waits for the first to commit or roll back and then reads the next offset
// the first left. Offsets are therefore assigned in commit order, and a
// reader never sees offset n+1 committed while n is not — it cannot skip a
// record that commits later. Appenders of different partitions do not wait
// on one another.
type sqlStream struct {
	tm  coresql.Transactor
	clk clock.Clock
	tx  sqlParts
	// wake is shared by every engine over the same pool and table in this
	// process.
	wake *wakeSignal
	// table is the records' table, which every storage failure names.
	table string
	// swept is, per partition, when this engine last released its expired
	// records, in Unix nanoseconds on its clock.
	swept  []atomic.Int64
	stmts  sqlStatements
	own    coresql.TxOptionsValue
	policy corestream.PolicyValue
	// turn counts the unkeyed batches, which go to the partitions in turn.
	turn atomic.Uint64
}

// NewSQL returns a stream kept in three tables of the caller's database,
// created by [SQLMigration]: the records, one head row per partition and the
// groups' cursors. It sends no statement: everything decidable without a
// database is decided here.
//
// An Append inside a transaction its context carries is a savepoint of that
// transaction (ADR 0151): its records exist if and only if the transaction
// commits, and the stream's Waker fires only after the commit. The head row
// of the partition is locked for the rest of that transaction, so the
// appenders of one partition are serialised and its offsets commit in
// order, with no gap a reader could skip. A Commit made on a handler's own
// transaction commits the group's offset with the handler's effect.
//
// It refuses a policy it cannot honour with the core's Validate, as every
// engine does, and a RetainBytes or MaxBytes, which it does not keep, with
// [corestream.SQLStreamMisconfigured]; a transactor, dialect or table it
// cannot use the same way. The stream it returns also answers
// [corestream.Waker], [corestream.Seeker], [corestream.Trimmer],
// [corestream.Compactor] and [corestream.Committer]; a compaction deletes
// the rows it drops in bounded statements.
func NewSQL(cfg SQLConfig) (s corestream.Stream, err error) {
	parts, invalid := cfg.validate()
	//: refused before anything is built.
	if invalid != nil {
		//: StreamMisconfigured or SQLStreamMisconfigured.
		return nil, invalid
	}
	policy := cfg.Policy.Normalized()
	//: usable.
	return &sqlStream{
		tm: cfg.Transactor, clk: sqlClockOrSystem(cfg.Clock), tx: parts,
		wake: sqlWakeFor(parts.join, cfg.Table), table: cfg.Table,
		swept: make([]atomic.Int64, policy.Partitions), stmts: renderSQLStatements(cfg.Dialect, cfg.Table),
		own: sqlOwnTxOptions(cfg.Dialect), policy: policy,
	}, nil
}

// sqlWakeFor returns the signal every engine over join's pool and table
// shares in this process — or one of its own when the pool cannot key a map.
func sqlWakeFor(join coresql.Joiner, table string) *wakeSignal {
	pool, _ := join.Join(context.Background())
	//: a key a map can hold without panicking.
	if pool == nil || !reflect.TypeOf(pool).Comparable() {
		//: this engine's own.
		return newWakeSignal()
	}
	key := sqlStreamKey{pool: pool, table: table}
	sqlWakes.mu.Lock()
	defer sqlWakes.mu.Unlock()
	signal, found := sqlWakes.signals[key]
	//: the first engine over this stream arms it.
	if !found {
		signal = newWakeSignal()
		sqlWakes.signals[key] = signal
	}
	//: the stream's.
	return signal
}

// sqlOwnTxOptions are the options of a transaction the engine opens itself:
// READ COMMITTED on MySQL, whose REPEATABLE READ takes gap locks no statement
// here needs (ADR 0139); every other engine keeps its default.
func sqlOwnTxOptions(dialect coresql.Dialect) coresql.TxOptionsValue {
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: no gap locks.
		return coresql.TxOptionsValue{Isolation: stdsql.LevelReadCommitted}
	}
	//: the engine's own default.
	return coresql.TxOptionsValue{}
}

// Partitions returns how many partitions the stream has.
func (s *sqlStream) Partitions() int {
	//: fixed at construction.
	return s.policy.Partitions
}

// Append writes records to one partition as one batch, in the transaction its
// context carries or in one of its own.
func (s *sqlStream) Append(
	ctx context.Context, records []corestream.RecordValue,
) (corestream.AppendValue, error) {
	//: a caller that has already given up writes nothing.
	if ctx.Err() != nil {
		//: the caller's own cause.
		return corestream.AppendValue{}, context.Cause(ctx)
	}
	//: the whole batch is refused before anything is sent.
	if _, refused := admit(records, s.policy.MaxRecordBytes); refused != nil {
		//: InvalidBatchSize or RecordTooLarge.
		return corestream.AppendValue{}, refused
	}
	partition, keyed, mixed := keyedPartition(records, s.policy.Partitions)
	//: a batch is one partition's.
	if mixed != nil {
		//: PartitionsMixed.
		return corestream.AppendValue{}, mixed
	}
	//: an unkeyed batch takes its turn.
	if !keyed {
		partition = int((s.turn.Add(1) - 1) % uint64(s.policy.Partitions))
	}
	now := s.clk.Now()
	//: an instant the table cannot hold is refused before a statement.
	if !sqlNameable(now) {
		//: StreamMisconfigured, naming the clock.
		return corestream.AppendValue{}, misconfigured("Clock")
	}
	var appended corestream.AppendValue
	writeErr := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		var err error
		appended, err = s.write(txCtx, ex, partition, records, now.UnixNano())
		//: the batch, or nothing.
		return err
	})
	//: nothing was appended.
	if writeErr != nil {
		//: StreamBackendFailed, or the transactor's own verdict.
		return corestream.AppendValue{}, writeErr
	}
	s.announce(ctx)
	s.sweep(ctx, partition, now)
	//: where the batch landed.
	return appended, nil
}

// write sends an Append's statements on ex: the head locked, then read, the
// records inserted a chunk at a time, the head moved past them. Every record
// of the batch is stamped with one instant, never before the partition's
// previous batch.
func (s *sqlStream) write(
	ctx context.Context, ex coresql.Executor, partition int, records []corestream.RecordValue, now int64,
) (corestream.AppendValue, error) {
	//: created when missing, and held until the transaction ends.
	if _, err := ex.ExecContext(ctx, s.stmts.lockHead, int64(partition)); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return corestream.AppendValue{}, s.failed("lock the head", err)
	}
	var next, first, last int64
	//: the head this transaction now holds.
	if err := ex.QueryRowContext(ctx, s.stmts.head, int64(partition)).Scan(&next, &first, &last); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return corestream.AppendValue{}, s.failed("read the head", err)
	}
	stamp := max(now, last)
	args := make([]any, 0, min(len(records), sqlRowsPerStatement)*sqlRecordArgs)
	//: one INSERT per chunk.
	for start := 0; start < len(records); start += sqlRowsPerStatement {
		chunk := records[start:min(start+sqlRowsPerStatement, len(records))]
		args = args[:0]
		//: five arguments per record, in placeholder order.
		for i, record := range chunk {
			args = append(args, int64(partition), next+int64(start+i), stamp, nullable(record.Key), nullable(record.Payload))
		}
		//: the chunk, or the batch fails whole.
		if _, err := ex.ExecContext(ctx, s.stmts.insertFor(len(chunk)), args...); err != nil {
			//: StreamBackendFailed, the driver's text withheld.
			return corestream.AppendValue{}, s.failed("insert records", err)
		}
	}
	end := next + int64(len(records))
	//: the next appender reads where this batch ends.
	if _, err := ex.ExecContext(ctx, s.stmts.advance, end, stamp, int64(partition)); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return corestream.AppendValue{}, s.failed("advance the head", err)
	}
	//: the batch's range.
	return corestream.AppendValue{
		Partition: partition, First: corestream.Offset(next), Last: corestream.Offset(end - 1),
	}, nil
}

// nullable binds an empty key or payload as NULL, which reads back as nil,
// as the memory engine keeps an empty one.
func nullable(b []byte) []byte {
	//: a nil []byte binds as NULL.
	if len(b) == 0 {
		return nil
	}
	//: the caller's bytes, copied by the driver.
	return b
}

// run runs a call's statements where they belong, as the queue's SQL broker
// does (ADR 0151): inside the caller's transaction every call is a savepoint
// of it, so its failure undoes it alone and leaves that transaction usable;
// outside one, a call of several statements runs in a transaction of the
// engine's own, and a call of one statement on the pool.
func (s *sqlStream) run(ctx context.Context, several bool, work coresql.TxFunc) error {
	ex, inTx := s.tx.join.Join(ctx)
	//: a savepoint takes no options: it has its transaction's.
	if inTx {
		//: a savepoint of the caller's transaction.
		return s.tm.Transact(ctx, coresql.TxOptionsValue{}, work)
	}
	//: one statement needs no transaction to be atomic.
	if !several {
		//: on the pool.
		return work(ctx, ex)
	}
	//: a transaction of the engine's own.
	return s.tm.Transact(ctx, s.own, work)
}

// announce wakes this stream's readers once the transaction ctx carries has
// committed — at once when the call ran in a transaction of its own, which
// has.
func (s *sqlStream) announce(ctx context.Context) {
	//: held until the caller's transaction commits, and dropped with it.
	if s.tx.hold.Defer(ctx, s.wake.fire) {
		//: the transactor fires it.
		return
	}
	//: already committed.
	s.wake.fire()
}

// failed is StreamBackendFailed for step, with the driver's error beside it
// and its text withheld by service/data/sql's Withheld: JOINED rather than
// wrapped, so the verdict's code stays the origin whatever the driver's chain
// holds, and a record's bytes a driver quotes never reach a rendering.
func (s *sqlStream) failed(step string, cause error) error {
	//: the verdict names the table and the step; the cause is reachable, and
	//: silent.
	return errors.Join(
		errs.Wrap(corestream.StreamBackendFailed, errs.WrapParams{}, errs.String("op", step), errs.String("table", s.table)),
		svcsql.NewWithheld(cause))
}

// sqlNameable reports whether an instant fits 64-bit Unix nanoseconds, the
// unit every instant of the tables is kept in.
func sqlNameable(t time.Time) bool {
	//: from the epoch to 2262-04-11.
	return !t.Before(time.Unix(0, 0)) && !t.After(time.Unix(0, math.MaxInt64))
}

// cutoff returns the instant a record must be appended after to be readable
// at now: RetainFor before now, or the dawn of int64 with no age bound.
func (s *sqlStream) cutoff(now time.Time) int64 {
	//: zero keeps every record for good.
	if s.policy.RetainFor == 0 {
		//: every stored instant is after it.
		return math.MinInt64
	}
	//: kept while younger than the bound.
	return now.UnixNano() - int64(s.policy.RetainFor)
}
