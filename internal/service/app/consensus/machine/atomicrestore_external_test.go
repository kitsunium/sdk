package machine_test

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcconsensus "github.com/kitsunium/sdk/internal/service/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/machine"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// errLoadCut is the failure a Load armed to fail returns half way.
var errLoadCut = errors.New("the database refused the load half way")

// cutLoad arms a configuration's Load to fail, once, after it wrote rows
// rows of the snapshot: the transaction holds them when it fails.
type cutLoad struct {
	armed atomic.Bool
	rows  int
	// fired is set once the armed Load failed.
	fired atomic.Bool
}

// wrap returns cfg whose Load fails once armed, after c.rows rows.
func (c *cutLoad) wrap(cfg machine.SQLConfig) machine.SQLConfig {
	load := cfg.Load
	cfg.Load = func(ctx context.Context, ex coresql.Executor, r io.Reader) error {
		//: a Load not armed reads the whole stream.
		if !c.armed.CompareAndSwap(true, false) {
			return load(ctx, ex, r)
		}
		var head bytes.Buffer
		// The first rows, a line each, go through the real Load in the
		// transaction, which then fails.
		for range c.rows {
			line, err := readLine(r)
			//: a stream shorter than the cut is the test's own error.
			if err != nil {
				return err
			}
			head.Write(line)
		}
		//: the rows written before the failure.
		if err := load(ctx, ex, &head); err != nil {
			return err
		}
		c.fired.Store(true)
		return errLoadCut
	}
	return cfg
}

// readLine reads one line of r, its newline included, a byte at a time so
// that nothing past it is consumed.
func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	var b [1]byte
	//: bytes up to the newline.
	for {
		//: a read that ends the stream before the newline.
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		line = append(line, b[0])
		//: the end of the line.
		if b[0] == '\n' {
			return line, nil
		}
	}
}

// A Restore whose Load fails half way, or whose position write fails,
// leaves the database and the position it reports as they were: the
// transaction rolls back, and the position moves only once it committed
// (#1607). A later restore of the same snapshot succeeds.
func TestAnSQLRestoreRefusedHalfWayLeavesTheStateAsItWas(t *testing.T) {
	type tc struct {
		name string
		// cut is the rows Load writes before it fails; zero when it does not.
		cut int
		// crashPosition fails the position write.
		crashPosition bool
	}
	runCase := func(t *testing.T, c tc) {
		_, srcTM := newDB(t)
		from := mustSQL(t, entriesConfig(srcTM))
		from.ApplyBatch(commands(1, 6), make([]any, 6))
		snap, err := from.Snapshot()
		//: the snapshot opens.
		if err != nil {
			t.Fatalf("Snapshot() = %v", err)
		}
		var body bytes.Buffer
		_, err = snap.WriteTo(&body)
		snap.Release()
		//: the snapshot writes.
		if err != nil {
			t.Fatalf("WriteTo() = %v", err)
		}
		d, tm := newDB(t)
		cl := &cutLoad{rows: c.cut}
		m := mustSQL(t, cl.wrap(entriesConfig(tm)))
		m.ApplyBatch(commands(1, 2), make([]any, 2))
		beforeRows := d.rows()
		beforeLI, beforePos, _ := m.AppliedPosition()
		meta := coreconsensus.SnapshotMetaValue{
			Position: coreconsensus.PositionValue{Term: 2, Index: 6},
			Log:      coreconsensus.LogIdentityValue{Cluster: testCluster, Epoch: 1},
		}
		cl.armed.Store(c.cut != 0)
		//: the position case fails the restore's own position write.
		if c.crashPosition {
			d.armCrash(6)
		}
		err = m.Restore(bytes.NewReader(body.Bytes()), meta)
		//: the restore is refused STATE_MACHINE_FAILED.
		if !errs.HasCode(err, coreconsensus.CodeStateMachineFailed) {
			t.Fatalf("Restore() = %v, want STATE_MACHINE_FAILED", err)
		}
		//: the armed Load wrote its rows before it failed.
		if c.cut != 0 && !cl.fired.Load() {
			t.Fatal("setup: the armed Load never failed")
		}
		//: the database holds what it held.
		if got := d.rows(); !slices.Equal(got, beforeRows) {
			t.Errorf("a refused restore left rows %+v, want %+v", got, beforeRows)
		}
		li, pos, _ := m.AppliedPosition()
		//: the position the machine reports is the one before.
		if li != beforeLI || pos != beforePos {
			t.Errorf("a refused restore moved the position to %+v %+v from %+v %+v", li, pos, beforeLI, beforePos)
		}
		_, dbPos, _ := mustSQL(t, entriesConfig(transactorOver(t, d))).AppliedPosition()
		//: the position the database holds is the one before.
		if dbPos != beforePos {
			t.Errorf("a refused restore left position %+v in the database, want %+v", dbPos, beforePos)
		}
		//: a later restore of the same snapshot succeeds.
		if err := m.Restore(bytes.NewReader(body.Bytes()), meta); err != nil {
			t.Fatalf("the restore after a refused one = %v", err)
		}
		//: and holds the snapshot's state.
		if rows := d.rows(); len(rows) != 6 || rows[5].idx != 6 {
			t.Errorf("the later restore holds %+v, want the snapshot's 6 rows", rows)
		}
	}
	//: each case restores a machine of its own.
	for _, c := range []tc{
		{name: "a Load that fails half way", cut: 3},
		{name: "a position write that fails", crashPosition: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// watchedSQL is an SQL machine that records, around every refused
// Restore, whether the database and the position it reports moved, and
// counts the restores that succeeded after one was refused. It embeds the
// machine, so it is an AtomicRestorer as SQL is.
type watchedSQL struct {
	*machine.SQL
	db *memDB
	// moved is set when a refused Restore left the state changed.
	moved *atomic.Bool
	// refused counts the refused restores, healed those that succeeded
	// after one.
	refused, healed *atomic.Int64
}

// Restore implements StateMachine, judging a refused restore.
func (w *watchedSQL) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	rows := w.db.rows()
	li, pos, _ := w.SQL.AppliedPosition()
	err := w.SQL.Restore(r, meta)
	//: a successful restore after a refused one is the later offer's.
	if err == nil {
		//: one refused before it.
		if w.refused.Load() > 0 {
			w.healed.Add(1)
		}
		return nil
	}
	w.refused.Add(1)
	li2, pos2, _ := w.SQL.AppliedPosition()
	//: the state is the one before the call.
	if !slices.Equal(w.db.rows(), rows) || li2 != li || pos2 != pos {
		w.moved.Store(true)
	}
	return err
}

// An SQL machine is an AtomicRestorer: a follower whose install's Restore
// fails half way in Load keeps running, on the state and the position it
// had applied, with no stop and no marker, and a later offer restores it;
// it then holds every command once, in log order (#1607). Before, the
// refusal stopped the node STATE_MACHINE_FAILED with a permanent marker
// although the transaction had rolled back (#1582).
func TestAnSQLFollowerWhoseInstallLoadFailsKeepsRunning(t *testing.T) {
	type tc struct {
		name string
		cut  int
	}
	runCase := func(t *testing.T, c tc) {
		dbs := make([]*memDB, 3)
		//: a database per node.
		for i := range dbs {
			dbs[i] = &memDB{}
		}
		cut := &cutLoad{rows: c.cut}
		var moved atomic.Bool
		var refused, healed atomic.Int64
		var victim atomic.Int64
		victim.Store(-1)
		var mu sync.Mutex
		cl := newTestClusterEvery(t, svcconsensus.DurabilityDurable, 20, func(i int) coreconsensus.StateMachine {
			mu.Lock()
			defer mu.Unlock()
			cfg := entriesConfig(transactorOver(t, dbs[i]))
			//: the victim's Load is the one armed.
			if int64(i) == victim.Load() {
				cfg = cut.wrap(cfg)
			}
			m, err := machine.NewSQL(context.Background(), cfg)
			//: the machine builds.
			if err != nil {
				t.Fatalf("NewSQL() = %v", err)
			}
			return &watchedSQL{SQL: m, db: dbs[i], moved: &moved, refused: &refused, healed: &healed}
		})
		var want []string
		write := func(from, to int) uint64 {
			var last uint64
			//: one command each.
			for k := from; k <= to; k++ {
				data := fmt.Sprintf("w%03d", k)
				tok, r := cl.propose([]byte(data))
				//: the machine answers the command.
				if r != data {
					t.Fatalf("propose %q answered %v", data, r)
				}
				want, last = append(want, data), tok.Index
			}
			return last
		}
		last := write(1, 30)
		li, _ := cl.leader()
		v := (li + 1) % 3
		cl.waitPosition(v, last)
		cl.kill(v)
		victim.Store(int64(v))
		last = write(31, 200)
		deadline := time.Now().Add(20 * time.Second)
		//: every live log starts past what the victim holds: only an install catches it up.
		for {
			behind := false
			//: the live nodes' logs.
			for i, m := range cl.members {
				//: the victim's own log does not count.
				if i == v {
					continue
				}
				//: a log that still reaches the victim's position.
				if first, _ := m.log.Bounds(); first <= 31 {
					behind = true
				}
			}
			//: compacted past.
			if !behind {
				break
			}
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatal("setup: the live logs never compacted past the victim")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cut.armed.Store(true)
		cl.start(v)
		deadline = time.Now().Add(20 * time.Second)
		//: the victim catches up through a later install.
		for {
			n, sm := cl.at(v)
			//: a stop is the defect.
			if reason, ok := n.StopReason(); ok {
				t.Fatalf("the follower stopped on a refused atomic restore: %v (refused %d)", reason.Err, refused.Load())
			}
			_, pos, _ := sm.(coreconsensus.DurableStateMachine).AppliedPosition()
			//: caught up.
			if pos.Index >= last {
				break
			}
			//: not in time.
			if time.Now().After(deadline) {
				t.Fatalf("the follower never caught up: position %d against %d, %d refused, %d healed, armed load fired %v",
					pos.Index, last, refused.Load(), healed.Load(), cut.fired.Load())
			}
			time.Sleep(5 * time.Millisecond)
		}
		//: the armed Load failed in an install.
		if !cut.fired.Load() || refused.Load() == 0 {
			t.Fatalf("setup: no install's restore was refused (fired %v, refused %d)", cut.fired.Load(), refused.Load())
		}
		//: the refusal left the database and the position as they were.
		if moved.Load() {
			t.Error("a refused restore moved the database or the position")
		}
		//: a later offer restored the follower.
		if healed.Load() == 0 {
			t.Error("no restore succeeded after the refused one")
		}
		rows := dbs[v].rows()
		//: every command once, in log order.
		if len(rows) != len(want) {
			t.Fatalf("the follower holds %d commands, want %d", len(rows), len(want))
		}
		//: each row.
		for k, r := range rows {
			//: the command of its rank, after the one before it.
			if r.data != want[k] || (k > 0 && r.idx <= rows[k-1].idx) {
				t.Fatalf("the follower's row %d is %+v, want %q in log order, once", k, r, want[k])
			}
		}
	}
	//: each case runs its own cluster.
	for _, c := range []tc{
		{name: "a Load that fails half way", cut: 20},
		{name: "a Load that fails at once"},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// lostAck is a Transactor whose next read-write Transact once armed runs
// and commits, then returns an error: the COMMIT reached the database and
// its acknowledgement was lost, which database/sql cannot tell from a
// COMMIT refused. With blind set, every Transact after it fails without
// running, so nothing can read what the database holds.
type lostAck struct {
	coresql.Transactor
	armed atomic.Bool
	blind atomic.Bool
	// twice runs the read-write fn of every Transact after the loss twice,
	// the second time on a cancelled context: a transactor that retries.
	twice atomic.Bool
	// lost is set once a commit's acknowledgement was lost.
	lost atomic.Bool
}

// Transact runs fn through the inner transactor, then loses its answer
// once armed.
func (l *lostAck) Transact(ctx context.Context, opts coresql.TxOptionsValue, fn coresql.TxFunc) error {
	//: a blind transactor reaches nothing once the acknowledgement was lost.
	if l.blind.Load() && l.lost.Load() {
		return errors.New("the database cannot be reached")
	}
	//: a retrying transactor whose second pass fails.
	if l.twice.Load() && l.lost.Load() && !opts.ReadOnly {
		return l.Transactor.Transact(ctx, opts, func(ctx context.Context, ex coresql.Executor) error {
			_ = fn(ctx, ex)
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			return fn(cctx, ex)
		})
	}
	err := l.Transactor.Transact(ctx, opts, fn)
	//: the armed read-write transaction committed, and its answer is lost.
	if err == nil && !opts.ReadOnly && l.armed.CompareAndSwap(true, false) {
		l.lost.Store(true)
		return errors.New("the connection broke after COMMIT was sent")
	}
	return err
}

// A Restore whose COMMIT reached the database but whose acknowledgement was
// lost restored the snapshot: the machine reads the position back and
// reports it, and the next batch follows the snapshot's state. Returned as
// a refusal with the old position, the node kept running and applied the
// entries after the old position over the snapshot's state (#1607). When
// the position cannot be read back the outcome is unknown: the machine
// reports no position, so the engine stops the node, and applies nothing.
func TestAnSQLRestoreWhoseCommitAcknowledgementIsLostIsJudgedOnTheDatabase(t *testing.T) {
	type tc struct {
		name string
		// blind fails the read back; twice runs it twice, the second pass
		// failing, which must not keep the first's answer.
		blind, twice bool
	}
	runCase := func(t *testing.T, c tc) {
		_, srcTM := newDB(t)
		from := mustSQL(t, entriesConfig(srcTM))
		from.ApplyBatch(commands(1, 6), make([]any, 6))
		snap, err := from.Snapshot()
		//: the snapshot opens.
		if err != nil {
			t.Fatalf("Snapshot() = %v", err)
		}
		var body bytes.Buffer
		_, err = snap.WriteTo(&body)
		snap.Release()
		//: the snapshot writes.
		if err != nil {
			t.Fatalf("WriteTo() = %v", err)
		}
		d, tm := newDB(t)
		la := &lostAck{Transactor: tm}
		la.blind.Store(c.blind)
		la.twice.Store(c.twice)
		m := mustSQL(t, entriesConfig(la))
		m.ApplyBatch(commands(1, 2), make([]any, 2))
		meta := coreconsensus.SnapshotMetaValue{
			Position: coreconsensus.PositionValue{Term: 2, Index: 6},
			Log:      coreconsensus.LogIdentityValue{Cluster: testCluster, Epoch: 1},
		}
		la.armed.Store(true)
		err = m.Restore(bytes.NewReader(body.Bytes()), meta)
		//: the acknowledgement was lost.
		if !la.lost.Load() {
			t.Fatal("setup: no acknowledgement was lost")
		}
		//: a read back that cannot reach the database leaves the outcome unknown.
		if c.blind || c.twice {
			//: a refusal.
			if err == nil {
				t.Fatal("a restore whose outcome is unknown returned nil")
			}
			//: and no position: the engine cannot take it for one kept.
			if _, pos, perr := m.AppliedPosition(); perr == nil {
				t.Errorf("a restore whose outcome is unknown left AppliedPosition %+v, nil", pos)
			}
			func() {
				defer func() {
					//: an apply over an unknown state panics.
					if r := recover(); r == nil {
						t.Error("a batch was applied over a state whose restore is unknown")
					}
				}()
				m.ApplyBatch(commands(7, 7), make([]any, 1))
			}()
			return
		}
		//: the restore took effect, and says so.
		if err != nil {
			t.Fatalf("a restore whose commit held returned %v", err)
		}
		li, pos, perr := m.AppliedPosition()
		//: the position is the snapshot's.
		if perr != nil || pos != meta.Position || li != meta.Log {
			t.Fatalf("after a restore whose commit held, AppliedPosition = %+v %+v %v, want %+v %+v", li, pos, perr, meta.Log, meta.Position)
		}
		m.ApplyBatch(commands(7, 7), make([]any, 1))
		rows := d.rows()
		//: the next batch follows the snapshot's six rows, once each.
		if len(rows) != 7 || rows[5].idx != 6 || rows[6].idx != 7 {
			t.Errorf("after a lost acknowledgement and one more batch the database holds %+v, want rows 1..7", rows)
		}
	}
	//: each case restores a machine of its own.
	for _, c := range []tc{
		{name: "the commit held, read back"},
		{name: "the read back fails too", blind: true},
		{name: "the read back's last pass fails", twice: true},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A Restore whose transaction reports a failure is read back on the
// primary, under the position row's lock: read on a replica that has not
// yet replayed the COMMIT, it saw the old position, and read without the
// lock while the COMMIT was still in flight, it saw it too; both refused,
// the server held the snapshot's state, and the next batch was applied
// over it (#1607). The read back waits for the COMMIT, then reports the
// snapshot's position, and the next batch follows it.
func TestAnSQLRestoreIsReadBackOnThePrimaryUnderTheRowLock(t *testing.T) {
	type tc struct {
		name string
		// replica routes read-only transactions to a replica left at the
		// old position, and loses the restore's COMMIT acknowledgement.
		replica bool
		// inflight has the restore's COMMIT answer an error, then commit
		// on the server after the delay.
		inflight time.Duration
		// bound replaces the read back's bound; a COMMIT in flight past
		// it leaves the outcome unknown.
		bound time.Duration
	}
	runCase := func(t *testing.T, c tc) {
		_, srcTM := newDB(t)
		from := mustSQL(t, entriesConfig(srcTM))
		from.ApplyBatch(commands(1, 6), make([]any, 6))
		snap, err := from.Snapshot()
		//: the snapshot opens.
		if err != nil {
			t.Fatalf("Snapshot() = %v", err)
		}
		var body bytes.Buffer
		_, err = snap.WriteTo(&body)
		snap.Release()
		//: the snapshot writes.
		if err != nil {
			t.Fatalf("WriteTo() = %v", err)
		}
		primary := &memDB{}
		var tm coresql.Transactor = transactorOver(t, primary)
		var la *lostAck
		var replica *memDB
		//: the replica case reads read-only transactions on a replica.
		if c.replica {
			replica = &memDB{}
			pdb, rdb := stdsql.OpenDB(primary), stdsql.OpenDB(replica)
			t.Cleanup(func() { _ = pdb.Close(); _ = rdb.Close() })
			rt, err := svcsql.NewTransactor(svcsql.Config{DB: pdb, ReadReplica: rdb, Dialect: coresql.DialectSQLite, Pool: svcsql.PoolConfig{MaxOpen: 4}})
			//: the transactor builds.
			if err != nil {
				t.Fatalf("NewTransactor() = %v", err)
			}
			la = &lostAck{Transactor: rt}
			tm = la
		}
		m := mustSQL(t, entriesConfig(tm))
		//: the bounded case waits less than the COMMIT takes.
		if c.bound > 0 {
			machine.SetReadBackTimeout(m, c.bound)
		}
		m.ApplyBatch(commands(1, 2), make([]any, 2))
		//: the replica holds what the primary held before the restore.
		if replica != nil {
			primary.mu.Lock()
			replica.committed = primary.committed.clone()
			primary.mu.Unlock()
			la.armed.Store(true)
		}
		//: the in-flight case's COMMIT lands after its answer.
		if c.inflight > 0 {
			primary.armInflight(c.inflight)
		}
		meta := coreconsensus.SnapshotMetaValue{
			Position: coreconsensus.PositionValue{Term: 2, Index: 6},
			Log:      coreconsensus.LogIdentityValue{Cluster: testCluster, Epoch: 1},
		}
		err = m.Restore(bytes.NewReader(body.Bytes()), meta)
		//: the setup's failure happened.
		if la != nil && !la.lost.Load() {
			t.Fatal("setup: no acknowledgement was lost")
		}
		//: a read back that met its bound names no outcome, and no position.
		if c.bound > 0 {
			<-primary.committedLate
			//: refused, with no position the engine could take for one kept.
			if _, _, perr := m.AppliedPosition(); err == nil || perr == nil {
				t.Errorf("a read back past its bound gave %v, AppliedPosition error %v; want both errors", err, perr)
			}
			return
		}
		//: the restore took effect on the primary, and says so.
		if err != nil {
			t.Fatalf("a restore the primary committed returned %v", err)
		}
		li, pos, perr := m.AppliedPosition()
		//: the position is the snapshot's.
		if perr != nil || pos != meta.Position || li != meta.Log {
			t.Fatalf("AppliedPosition = %+v %+v %v, want %+v %+v", li, pos, perr, meta.Log, meta.Position)
		}
		m.ApplyBatch(commands(7, 7), make([]any, 1))
		rows := primary.rows()
		//: the next batch follows the snapshot's six rows, once each.
		if len(rows) != 7 || rows[5].idx != 6 || rows[6].idx != 7 {
			t.Errorf("the primary holds %+v, want rows 1..7", rows)
		}
	}
	//: each case restores a machine of its own.
	for _, c := range []tc{
		{name: "a replica behind the primary", replica: true},
		{name: "a COMMIT still in flight", inflight: 50 * time.Millisecond},
		{name: "a COMMIT in flight past the bound", inflight: 300 * time.Millisecond, bound: 30 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
