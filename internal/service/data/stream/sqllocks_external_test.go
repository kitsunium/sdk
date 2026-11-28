// Package stream_test — the fake engine's concurrent mode: transactions side
// by side, and the head's row locks as each engine takes them.
//
// # What InnoDB does to concurrent inserts of one missing key
//
// MySQL 8.0's manual, 17.7.3 "Locks Set by Different SQL Statements in
// InnoDB": an INSERT sets an exclusive index-record lock on the inserted
// row, after an insert intention gap lock; a second INSERT of the same key
// gets a duplicate-key error and waits for a lock on that record — a shared
// one, an exclusive one for INSERT ... ON DUPLICATE KEY UPDATE on a primary
// key. When the first rolls back, its row is gone and the waiters'
// requests stay on its place in the index: 17.7.1 says conflicting locks
// may be held on a gap "if a record is purged from an index", that gap locks
// "are purely inhibitive [...] prevent other transactions from inserting to
// the gap", and 17.7.2.1 that even READ COMMITTED keeps gap locking for
// "duplicate-key checking". Each waiter then inserts the key itself, and its
// insert intention waits for the gap the others hold: two waiters are a
// cycle, the deadlock the manual's three-session example ends in. 17.7.5.2
// rolls back "a transaction or transactions to break the deadlock", 17.21.5
// "the entire transaction", and 15.3.4 answers a later ROLLBACK TO SAVEPOINT
// with "ERROR 1305 (42000): SAVEPOINT identifier does not exist" — exactly
// what e2e/integration/sql's TestConcurrentAppendersCommitInOrderOnEveryEngine
// reported on MySQL (#822): STREAM_BACKEND_FAILED, then SAVEPOINT_FAILED.
//
// The model keeps that much and no more: the inheritance is MySQL's alone,
// PostgreSQL's waiter inserts the key once the first rolls back, and an
// autocommit statement holds its lock for itself alone.
package stream_test

import (
	"database/sql/driver"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// errDeadlock is MySQL's answer to the victim of a deadlock, whose whole
// transaction it has rolled back.
var errDeadlock = errors.New("Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction")

// headLock is one partition's head row as the lock manager sees it.
type headLock struct {
	// holder holds the row's exclusive lock, nil when nobody does.
	holder *fakeSQLTx
	// fresh is whether holder inserted the row, not yet committed.
	fresh bool
	// waiting are the transactions whose insert of the key waits on the
	// fresh row, and gaps those whose wait outlived its rollback: the gap
	// locks left where the row was (MySQL's).
	waiting, gaps map[*fakeSQLTx]bool
	// inserting are the transactions whose insert intention waits on a gap
	// another transaction holds.
	inserting map[*fakeSQLTx]bool
	// queued are the transactions waiting for holder, whatever they asked.
	queued map[*fakeSQLTx]bool
}

// newConcurrentSQLEngine returns an empty engine whose transactions run side
// by side, with the head's row locks or, rowLocks false, none. Its pool
// holds 16 connections, more than the appenders a race runs, since on MySQL
// each may create a partition's head on a second one.
func newConcurrentSQLEngine(dialect coresql.Dialect, table string, rowLocks bool) *sqlEngine {
	e := newSQLEngine(dialect, table)
	e.concurrent, e.rowLocks, e.locks, e.maxOpen = true, rowLocks, map[int64]*headLock{}, 16
	e.unlocked = sync.NewCond(&e.mu)
	return e
}

// lockOf returns partition part's head lock, made on first use. e.mu is held.
func (e *sqlEngine) lockOf(part int64) *headLock {
	l, found := e.locks[part]
	//: the first statement that names the partition.
	if !found {
		l = &headLock{
			waiting: map[*fakeSQLTx]bool{}, gaps: map[*fakeSQLTx]bool{},
			inserting: map[*fakeSQLTx]bool{}, queued: map[*fakeSQLTx]bool{},
		}
		e.locks[part] = l
	}
	return l
}

// locksHead reports whether a statement of role locks a partition's head on
// a concurrent engine, which, and whether it inserts the row when missing:
// the upsert and the creation insert it; the locking read, where the dialect
// has one, and the updates lock an existing row.
func (e *sqlEngine) locksHead(role string, args []driver.Value) (part int64, inserts, locks bool) {
	//: the serialising engine, and the negative control, lock nothing.
	if !e.concurrent || !e.rowLocks {
		return 0, false, false
	}
	switch role {
	//: an insert of the head's key.
	case "lockHead", "createHead":
		return args[0].(int64), true, true
	//: FOR UPDATE, which SQLite does not spell.
	case "head":
		return args[0].(int64), false, e.dialect != coresql.DialectSQLite
	//: the head moved past a batch.
	case "advance":
		return args[2].(int64), false, true
	//: the head's floor raised.
	case "floor":
		return args[1].(int64), false, true
	}
	return 0, false, false
}

// acquire takes partition part's head lock for t, waiting while another
// transaction holds it; inserts is whether t's statement inserts a missing
// row. It answers errDeadlock, having rolled t back whole, when t's insert
// would wait for a gap whose holder waits for t's. e.mu is held.
func (e *sqlEngine) acquire(t *fakeSQLTx, part int64, inserts bool) error {
	l := e.lockOf(part)
	for {
		_, exists := e.committed.heads[part]
		switch {
		//: already t's.
		case l.holder == t:
			return nil
		//: another transaction's row: a duplicate check waits on a fresh one.
		case l.holder != nil:
			//: the wait InnoDB leaves on the gap should the row roll back.
			if l.fresh && inserts {
				l.waiting[t] = true
			}
			l.queued[t] = true
			e.unlocked.Wait()
			delete(l.queued, t)
			continue
		//: a committed row, or a locking read of a missing one.
		case exists || !inserts:
			//: only an existing row is locked.
			if exists {
				l.holder = t
			}
			return nil
		//: t inserts into a gap another transaction holds.
		case blocked(l, t):
			//: a cycle: t holds a gap whose holder waits on t's.
			if l.gaps[t] && waitsOn(l, t) {
				e.deadlocks++
				e.kill(t)
				return errDeadlock
			}
			l.inserting[t] = true
			e.unlocked.Wait()
			delete(l.inserting, t)
			continue
		}
		l.holder, l.fresh = t, true
		return nil
	}
}

// blocked reports whether a transaction other than t holds a gap lock on
// l's key, which t's insert intention waits on.
func blocked(l *headLock, t *fakeSQLTx) bool {
	return slices.ContainsFunc(slices.Collect(maps.Keys(l.gaps)), func(u *fakeSQLTx) bool { return u != t })
}

// waitsOn reports whether a transaction whose gap t's insert waits on waits
// itself to insert l's key, on the gap t holds: the cycle.
func waitsOn(l *headLock, t *fakeSQLTx) bool {
	return slices.ContainsFunc(slices.Collect(maps.Keys(l.inserting)), func(u *fakeSQLTx) bool { return u != t && l.gaps[u] })
}

// release gives back every head lock t holds, committed or not: an insert
// rolled back leaves, on MySQL, its waiters' locks on the gap. e.mu is held.
func (e *sqlEngine) release(t *fakeSQLTx, committed bool) {
	//: the serialising engine keeps no lock table.
	if !e.concurrent {
		return
	}
	for _, l := range e.locks {
		//: t's own row.
		if l.holder == t {
			//: InnoDB's rolled-back insert: the waiters keep the gap.
			if l.fresh && !committed && e.dialect == coresql.DialectMySQL {
				maps.Copy(l.gaps, l.waiting)
			}
			l.holder, l.fresh = nil, false
			clear(l.waiting)
		}
		delete(l.gaps, t)
		delete(l.waiting, t)
		delete(l.inserting, t)
	}
	e.unlocked.Broadcast()
}

// awaitQueued waits until n transactions wait for partition part's head
// held by another, and reports whether they did within a few seconds.
func (e *sqlEngine) awaitQueued(part int64, n int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		queued := len(e.lockOf(part).queued)
		e.mu.Unlock()
		//: the waiters are in place.
		if queued >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// kill rolls t back whole, as a deadlock's victim: its writes and savepoints
// are gone, its locks given back. e.mu is held.
func (e *sqlEngine) kill(t *fakeSQLTx) {
	t.killed, t.savepoints, t.delta = true, nil, newStreamDelta()
	e.release(t, false)
}

// runConcurrent executes one statement of t on a concurrent engine: the
// head's lock first, then the statement over what is committed now and t's
// own writes. e.mu is held.
func (t *fakeSQLTx) runConcurrent(role string, args []driver.Value) (answer, error) {
	e := t.conn.engine
	//: the lock the statement takes, held until t ends.
	if part, inserts, locks := e.locksHead(role, args); locks {
		//: t is the deadlock's victim.
		if err := e.acquire(t, part, inserts); err != nil {
			return answer{}, err
		}
	}
	t.work = t.delta.over(e.committed)
	ans, err := execute(role, args, t.work)
	//: PostgreSQL aborts the transaction on a failed statement.
	if err != nil && e.dialect == coresql.DialectPostgres {
		t.aborted = true
	}
	t.delta = diff(t.work, e.committed)
	return ans, err
}

// publish applies a concurrent transaction's writes to the committed tables,
// refusing the whole commit when a record it inserted was committed by
// another transaction meanwhile, as a unique key would. e.mu is held.
func (t *fakeSQLTx) publish() error {
	e := t.conn.engine
	for k := range t.delta.records {
		//: the offset another appender committed first.
		if _, taken := e.committed.records[k]; taken {
			return errors.New("duplicate key value violates unique constraint at commit")
		}
	}
	e.committed = t.delta.over(e.committed)
	return nil
}

// streamDelta is a transaction's writes over the committed tables.
type streamDelta struct {
	records map[recordKey]recordRow
	deleted map[recordKey]bool
	heads   map[int64]headRow
	cursors map[string]int64
}

// newStreamDelta returns a delta that writes nothing.
func newStreamDelta() streamDelta {
	return streamDelta{records: map[recordKey]recordRow{}, deleted: map[recordKey]bool{}, heads: map[int64]headRow{}, cursors: map[string]int64{}}
}

// clone copies d, so a savepoint can be undone.
func (d streamDelta) clone() streamDelta {
	return streamDelta{records: maps.Clone(d.records), deleted: maps.Clone(d.deleted), heads: maps.Clone(d.heads), cursors: maps.Clone(d.cursors)}
}

// over returns committed with d applied.
func (d streamDelta) over(committed streamTables) streamTables {
	v := committed.clone()
	for k := range d.deleted {
		delete(v.records, k)
	}
	maps.Copy(v.records, d.records)
	maps.Copy(v.heads, d.heads)
	maps.Copy(v.cursors, d.cursors)
	return v
}

// diff returns what view holds that committed does not, as a delta.
func diff(view, committed streamTables) streamDelta {
	d := newStreamDelta()
	for k, r := range view.records {
		//: a record t inserted or rewrote.
		if c, found := committed.records[k]; !found || c.at != r.at || !slices.Equal(c.key, r.key) || !slices.Equal(c.payload, r.payload) {
			d.records[k] = r
		}
	}
	for k := range committed.records {
		//: a record t deleted.
		if _, found := view.records[k]; !found {
			d.deleted[k] = true
		}
	}
	for k, h := range view.heads {
		//: a head t created or moved.
		if c, found := committed.heads[k]; !found || c != h {
			d.heads[k] = h
		}
	}
	for k, n := range view.cursors {
		//: a cursor t committed.
		if c, found := committed.cursors[k]; !found || c != n {
			d.cursors[k] = n
		}
	}
	return d
}
