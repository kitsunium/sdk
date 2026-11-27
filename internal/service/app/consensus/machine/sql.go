package machine

import (
	"context"
	stdsql "database/sql"
	"encoding/hex"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultPositionTable is the table SQLConfig.Table names when it is empty.
const DefaultPositionTable string = "kit_consensus_applied"

// readBackTimeout bounds the read back of a Restore whose transaction
// reported a failure: the wait for the position row's lock, which a COMMIT
// still in flight holds. It exceeds the 10 s after which MySQL's
// semi-synchronous replication gives up on its replica and lets a held
// COMMIT end (rpl_semi_sync_master_timeout's default), and a healthy
// server's group fsync by far; past it the outcome is unknown and the
// engine stops the node, the safe side, rather than keep the apply
// goroutine waiting on a server that may never answer.
const readBackTimeout time.Duration = 15 * time.Second

// maxTableName bounds a position table's name: PostgreSQL's 63 bytes, the
// shortest identifier limit of the three dialects.
const maxTableName int = 63

// sqlSnapshotMagic and sqlSnapshotVersion open every snapshot an SQL
// machine writes: four bytes and a format version, then what Dump wrote.
const (
	sqlSnapshotMagic        = "KSQL"
	sqlSnapshotVersion byte = 1
)

// positionColumns are the columns of the position row, in the order every
// statement binds and scans them.
const positionColumns = "cluster, epoch, predecessor, predecessor_epoch, kept_index, adopted_from, applied_term, applied_index"

// SQLConfig builds an SQL machine. Every field but Table is required: the
// machine runs the caller's statements and owns the position table alone.
// The machine assumes a durable COMMIT (PostgreSQL's synchronous_commit
// on, MySQL's innodb_flush_log_at_trx_commit=1 and sync_binlog=1,
// SQLite without synchronous=OFF) and a failover to a synchronous copy
// only: otherwise a committed batch or restore can be lost, and neither
// DurabilityDurable nor the AtomicRestorer promise holds. Under MySQL
// the read back of a failed restore runs at READ COMMITTED, which
// MySQL admits only with binlog_format=ROW: under another format every
// failed restore ends in an unknown outcome, and the node stops with
// its marker.
type SQLConfig struct {
	// Transactor runs the machine's transactions.
	Transactor coresql.Transactor
	// Dialect spells the position table's statements.
	Dialect coresql.Dialect
	// Table names the position table, a bare lower-case identifier of at
	// most 63 bytes; empty is DefaultPositionTable.
	Table string
	// Apply applies one committed command on ex, the batch's transaction,
	// and returns its result. A refusal is a result; an error means the
	// command cannot complete, and stops the node.
	Apply func(ctx context.Context, ex coresql.Executor, e coreconsensus.EntryValue) (any, error)
	// Dump writes the machine's state, read on ex, the snapshot's read-only
	// transaction.
	Dump func(ctx context.Context, ex coresql.Executor, w io.Writer) error
	// Truncate removes the machine's state on ex, before a Load.
	Truncate func(ctx context.Context, ex coresql.Executor) error
	// Load writes on ex the state Dump wrote into r.
	Load func(ctx context.Context, ex coresql.Executor, r io.Reader) error
}

// SQL is a durable, batching state machine over a database: the state is
// the caller's tables, the applied position one row of its own, written
// in the transaction of the batch it ends.
type SQL struct {
	cfg SQLConfig
	// sel, upd and ins are the position row's three statements, rendered
	// once for the dialect.
	sel, upd, ins string
	// snapIso is the isolation a snapshot's transaction reads at.
	snapIso stdsql.IsolationLevel
	// lockRow takes the position row's lock without changing it, and
	// lockIso is the isolation its read back runs at (readBack).
	lockRow string
	lockIso stdsql.IsolationLevel
	// readBackWait bounds readBack: readBackTimeout, which a test lowers.
	readBackWait time.Duration

	mu  sync.Mutex
	li  coreconsensus.LogIdentityValue
	pos coreconsensus.PositionValue
	// unknown is set when a Restore's outcome could not be read back: the
	// database holds the old state or the snapshot's, and the machine does
	// not know which. Until a Restore succeeds, AppliedPosition and
	// Snapshot return it and ApplyBatch panics with it (#1607).
	unknown error
}

// NewSQL builds an SQL machine: it creates the position table when it is
// absent, with a zero position row, and reads the position the database
// holds. A configuration it cannot run is CONSENSUS_MISCONFIGURED; a
// database that fails is STATE_MACHINE_FAILED.
func NewSQL(ctx context.Context, cfg SQLConfig) (*SQL, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	m := &SQL{cfg: cfg, snapIso: stdsql.LevelRepeatableRead, lockIso: stdsql.LevelReadCommitted, readBackWait: readBackTimeout}
	if cfg.Dialect == coresql.DialectSQLite {
		//: SQLite isolates a transaction serialisably and knows no other level.
		m.snapIso, m.lockIso = stdsql.LevelSerializable, stdsql.LevelDefault
	}
	t := cfg.Dialect.QuoteIdent(cfg.Table)
	m.lockRow = "UPDATE " + t + " SET id = id WHERE id = 1"
	ph := cfg.Dialect.Placeholder
	m.sel = "SELECT " + positionColumns + " FROM " + t + " WHERE id = 1"
	m.upd = "UPDATE " + t + " SET cluster = " + ph(1) + ", epoch = " + ph(2) + ", predecessor = " + ph(3) +
		", predecessor_epoch = " + ph(4) + ", kept_index = " + ph(5) + ", adopted_from = " + ph(6) +
		", applied_term = " + ph(7) + ", applied_index = " + ph(8) + " WHERE id = 1"
	m.ins = "INSERT INTO " + t + " (id, " + positionColumns + ") VALUES (1, " + ph(1) + ", " + ph(2) + ", " +
		ph(3) + ", " + ph(4) + ", " + ph(5) + ", " + ph(6) + ", " + ph(7) + ", " + ph(8) + ")"
	create := "CREATE TABLE IF NOT EXISTS " + t + " (id INTEGER NOT NULL PRIMARY KEY, cluster VARCHAR(32) NOT NULL," +
		" epoch BIGINT NOT NULL, predecessor VARCHAR(32) NOT NULL, predecessor_epoch BIGINT NOT NULL," +
		" kept_index BIGINT NOT NULL, adopted_from BIGINT NOT NULL, applied_term BIGINT NOT NULL," +
		" applied_index BIGINT NOT NULL)"
	err := cfg.Transactor.Transact(ctx, coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		if _, err := ex.ExecContext(ctx, create); err != nil {
			return err
		}
		li, pos, found, err := m.readPosition(ctx, ex)
		if err != nil {
			return err
		}
		if !found {
			//: a new table: the zero position, so every later write is one UPDATE.
			_, err = ex.ExecContext(ctx, m.ins, positionArgs(li, pos)...)
			return err
		}
		m.li, m.pos = li, pos
		return nil
	})
	if err != nil {
		return nil, failed("open", err)
	}
	return m, nil
}

// validate refuses a configuration the machine cannot run.
func (c *SQLConfig) validate() error {
	if c.Table == "" {
		c.Table = DefaultPositionTable
	}
	if err := c.validateParts(); err != nil {
		return err
	}
	if !bareIdent(c.Table) {
		return misconfigured("Table is not a bare identifier of at most 63 bytes")
	}
	return nil
}

// validateParts refuses a missing transactor, dialect or callback.
func (c *SQLConfig) validateParts() error {
	switch {
	case c.Transactor == nil:
		return misconfigured("Transactor is nil")
	case !c.Dialect.Valid():
		return misconfigured("Dialect is not one this SDK spells")
	case c.Apply == nil || c.Dump == nil || c.Truncate == nil || c.Load == nil:
		return misconfigured("Apply, Dump, Truncate and Load are all required")
	}
	return nil
}

// bareIdent reports whether s is [a-z_][a-z0-9_]* of at most maxTableName
// bytes: a name that needs no quoting in any dialect, and so reads the same
// in all three.
func bareIdent(s string) bool {
	if s == "" || len(s) > maxTableName || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := range len(s) {
		if !identByte(s[i]) {
			return false
		}
	}
	return true
}

// identByte reports whether c is one of [a-z0-9_].
func identByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}

// Apply applies one committed command: ApplyBatch of one entry.
func (m *SQL) Apply(e coreconsensus.EntryValue) any {
	var r [1]any
	m.ApplyBatch([]coreconsensus.EntryValue{e}, r[:])
	return r[0]
}

// ApplyBatch applies es in one transaction, writes the position of the
// last one in that same transaction, commits, then fills results. It
// panics when the batch cannot complete (D3), and when a Restore's
// outcome is unknown.
func (m *SQL) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	if len(es) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: D3: a state the machine cannot name is never applied over.
	if m.unknown != nil {
		panic(m.unknown)
	}
	last := es[len(es)-1]
	li := m.li
	if !li.Same(coreconsensus.LogIdentityValue{Cluster: last.Token.Cluster, Epoch: last.Token.Epoch}) {
		//: a new history: its identity is the token's, its lineage unknown here.
		li = coreconsensus.LogIdentityValue{Cluster: last.Token.Cluster, Epoch: last.Token.Epoch}
	}
	out := make([]any, len(es))
	err := m.cfg.Transactor.Transact(context.Background(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		for i := range es {
			r, err := m.cfg.Apply(ctx, ex, es[i])
			if err != nil {
				return err
			}
			out[i] = r
		}
		return m.writePosition(ctx, ex, li, last.Position)
	})
	if err != nil {
		//: D3: an Apply that cannot complete panics, and the node stops; the
		//: transaction rolled back, so a restart replays the whole batch.
		panic(failed("apply", err))
	}
	m.li, m.pos = li, last.Position
	copy(results, out)
}

// AppliedPosition returns the log identity and the position the database
// holds applied. After a Restore whose outcome is unknown it returns
// STATE_MACHINE_FAILED, until a Restore succeeds.
func (m *SQL) AppliedPosition() (coreconsensus.LogIdentityValue, coreconsensus.PositionValue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a restore whose outcome is unknown names no position.
	if m.unknown != nil {
		return coreconsensus.LogIdentityValue{}, coreconsensus.PositionValue{}, m.unknown
	}
	return m.li, m.pos, nil
}

// Restore replaces the state with a snapshot's: Truncate, Load and the
// snapshot's position, in one transaction. A snapshot no SQL machine
// wrote is SNAPSHOT_CORRUPT, one of a newer format VERSION_UNSUPPORTED,
// both refused before the transaction opens. A refusal leaves the
// database and the position as they were: whatever fails in the
// transaction rolls it back, and the position the machine reports
// moves only once it committed. A transaction that returned an error
// is judged on the position row, because a COMMIT whose
// acknowledgement was lost, or that the server still runs, commits
// although it reports a failure. The row is read back on the primary,
// in a read-write transaction that a ReadReplica never serves: it
// first takes the row's lock, which waits for a COMMIT in flight,
// then reads the row at READ COMMITTED and rolls back, within 15
// seconds. The snapshot's position is a restore
// that took effect, and Restore returns nil; the position before, a
// refusal; anything else, or no answer, an outcome the machine cannot
// name, after which AppliedPosition and Snapshot refuse and
// ApplyBatch panics, so that the engine stops the node.
func (m *SQL) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	var head [len(sqlSnapshotMagic) + 1]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return corrupt("the snapshot is shorter than its header")
	}
	if string(head[:len(sqlSnapshotMagic)]) != sqlSnapshotMagic {
		return corrupt("the snapshot is not one an SQL machine wrote")
	}
	if v := head[len(sqlSnapshotMagic)]; v != sqlSnapshotVersion {
		return unsupported("sql machine snapshot", v)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.cfg.Transactor.Transact(context.Background(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
		if err := m.cfg.Truncate(ctx, ex); err != nil {
			return err
		}
		if err := m.cfg.Load(ctx, ex, r); err != nil {
			return err
		}
		return m.writePosition(ctx, ex, meta.Log, meta.Position)
	})
	//: a failed transaction is judged on what the database holds.
	if err != nil {
		return m.restoreOutcome(&meta, err)
	}
	m.li, m.pos, m.unknown = meta.Log, meta.Position, nil
	return nil
}

// restoreOutcome judges a Restore whose transaction returned cause on the
// position the database holds, read back in a transaction of its own: a
// COMMIT whose acknowledgement was lost committed although database/sql
// reports it failed, and returned as a refusal with the old position, the
// engine applied the entries after that position over the snapshot's state
// (#1607). The snapshot's position is a restore that took effect, nil; the
// old one a refusal that left the state as it was; anything else, or no
// answer, an outcome the machine cannot name, kept in unknown so that the
// engine, reading AppliedPosition back, stops the node. The caller holds mu.
func (m *SQL) restoreOutcome(meta *coreconsensus.SnapshotMetaValue, cause error) error {
	li, pos, rerr := m.readBack()
	//: the transaction committed: the snapshot is the state.
	if rerr == nil && li == meta.Log && pos == meta.Position {
		m.li, m.pos, m.unknown = meta.Log, meta.Position, nil
		return nil
	}
	//: the transaction rolled back: the state is the one before.
	if rerr == nil && li == m.li && pos == m.pos {
		return failed("restore", cause)
	}
	f := []errs.FieldValue{errs.String("op", "restore"), errs.String("why", "the outcome of the restore's transaction is unknown"), errs.String("cause", cause.Error())}
	//: the read back's own failure is kept beside the cause.
	if rerr != nil {
		f = append(f, errs.String("readback", rerr.Error()))
	}
	m.unknown = origin(coreconsensus.StateMachineFailed, f...)
	return m.unknown
}

// readBack reads the position row as the primary holds it once no
// transaction can still commit over it: in a read-write transaction, so
// that a transactor with a ReadReplica does not route it to a replica that
// has not replayed the COMMIT, which first takes the row's lock, which the
// restore's transaction holds until the server ends it, then reads the
// row, at READ COMMITTED, as the holder left it, then rolls back; within
// readBackTimeout. Read on a replica, or without the lock while a COMMIT
// was in flight, it saw the old position and the restore was taken for a
// refusal over the snapshot's state (#1607). PostgreSQL and MySQL make the
// UPDATE wait for the holder and read the version it committed; SQLite
// commits in the call, so nothing is in flight there. A database with no
// row, a wait that ends, or any failure answers an error: the outcome is
// unknown.
func (m *SQL) readBack() (coreconsensus.LogIdentityValue, coreconsensus.PositionValue, error) {
	var li coreconsensus.LogIdentityValue
	var pos coreconsensus.PositionValue
	ctx, cancel := context.WithTimeout(context.Background(), m.readBackWait)
	defer cancel()
	read := false
	err := m.cfg.Transactor.Transact(ctx, coresql.TxOptionsValue{Isolation: m.lockIso}, func(ctx context.Context, ex coresql.Executor) error {
		// A transactor that runs fn again keeps no answer of an earlier pass.
		read = false
		//: the row's lock, waited for behind a COMMIT in flight.
		if _, err := ex.ExecContext(ctx, m.lockRow); err != nil {
			return err
		}
		l, p, found, err := m.readPosition(ctx, ex)
		//: a position row that is not there names no outcome.
		if err == nil && !found {
			return origin(coreconsensus.StateMachineFailed, errs.String("why", "the position row is not there"))
		}
		//: a read that failed.
		if err != nil {
			return err
		}
		li, pos, read = l, p, true
		// The read is all this transaction is for: an error rolls it back.
		return origin(coreconsensus.StateMachineFailed, errs.String("why", "a read back rolls back"))
	})
	//: the read happened under the lock; the rollback that followed decides nothing.
	if read {
		return li, pos, nil
	}
	return li, pos, err
}

// RestoresAtomically marks SQL as a coreconsensus.AtomicRestorer: its
// Restore is one transaction, whose outcome it reads back when the
// transaction reports a failure, so the engine keeps a node running
// when it refuses. A type that embeds SQL inherits the marker, and
// must keep its own Restore atomic. It does nothing.
func (m *SQL) RestoresAtomically() {}

// writePosition updates the one position row inside the transaction ex
// belongs to.
func (m *SQL) writePosition(ctx context.Context, ex coresql.Executor, li coreconsensus.LogIdentityValue, pos coreconsensus.PositionValue) error {
	res, err := ex.ExecContext(ctx, m.upd, positionArgs(li, pos)...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return origin(coreconsensus.StateMachineFailed, errs.String("why", "the position row is not there"), errs.Int64("rows", n))
	}
	return nil
}

// readPosition reads the position row; found is false when there is none.
func (m *SQL) readPosition(ctx context.Context, ex coresql.Executor) (li coreconsensus.LogIdentityValue, pos coreconsensus.PositionValue, found bool, err error) {
	var cluster, pred string
	var v [6]int64
	err = ex.QueryRowContext(ctx, m.sel).Scan(&cluster, &v[0], &pred, &v[1], &v[2], &v[3], &v[4], &v[5])
	if err == stdsql.ErrNoRows {
		return li, pos, false, nil
	}
	if err != nil {
		return li, pos, false, err
	}
	for _, x := range v {
		if x < 0 {
			return li, pos, false, corrupt("the position row holds a negative number")
		}
	}
	if li.Cluster, err = clusterOf(cluster); err != nil {
		return li, pos, false, err
	}
	if li.Predecessor, err = clusterOf(pred); err != nil {
		return li, pos, false, err
	}
	li.Epoch, li.PredecessorEpoch, li.KeptIndex, li.AdoptedFrom = uint64(v[0]), uint64(v[1]), uint64(v[2]), uint64(v[3])
	pos = coreconsensus.PositionValue{Term: uint64(v[4]), Index: uint64(v[5])}
	return li, pos, true, nil
}

// clusterOf decodes a cluster id the position row spells in hex.
func clusterOf(s string) (coreconsensus.ClusterID, error) {
	var c coreconsensus.ClusterID
	if n, err := hex.Decode(c[:], []byte(strings.TrimSpace(s))); err != nil || n != len(c) {
		return c, corrupt("the position row holds a cluster id that is not 32 hex digits")
	}
	return c, nil
}

// positionArgs are the eight values of the position row, in column order.
// A number above math.MaxInt64 has no BIGINT: the machine stops on it.
func positionArgs(li coreconsensus.LogIdentityValue, pos coreconsensus.PositionValue) []any {
	nums := [6]uint64{li.Epoch, li.PredecessorEpoch, li.KeptIndex, li.AdoptedFrom, pos.Term, pos.Index}
	for _, x := range nums {
		if x > math.MaxInt64 {
			panic(origin(coreconsensus.StateMachineFailed, errs.String("why", "a position past 2^63")))
		}
	}
	return []any{
		li.Cluster.String(), int64(nums[0]), li.Predecessor.String(), int64(nums[1]),
		int64(nums[2]), int64(nums[3]), int64(nums[4]), int64(nums[5]),
	}
}
