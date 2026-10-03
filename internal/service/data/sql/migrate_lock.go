// Package sql — hosts the migration lock: why it is the engine's own advisory
// lock, and why it lives on a connection of its own.
package sql

import (
	"context"
	stdsql "database/sql"
	"errors"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// lockPhase labels every error raised while taking or releasing the migration
// lock, so a failed run says which phase it died in.
const lockPhase string = "lock"

// advisoryLock is a held migration lock: the connection that holds it, and
// the statement that gives it back.
//
// The connection is DEDICATED and is not the pool. Both mechanisms this
// package uses — pg_advisory_lock and GET_LOCK — are scoped to a SESSION, so
// releasing from a pooled connection would release nothing on a different
// session and report success. Holding one connection for the duration of the
// run is the price of a lock that is actually held.
type advisoryLock struct {
	// conn is the session that holds the lock.
	conn *stdsql.Conn
	// query is the pre-rendered release statement.
	query string
	// arg is the pre-rendered release argument.
	arg any
}

// acquire takes the migration lock, retrying until the budget expires.
//
// # Why the engine's advisory lock and not a lock table, or the SDK's own lock domain
//
// The property that decides it is what happens when the holder DIES. A
// migration runner is killed mid-run more often than any other piece of a
// deployment — an OOM kill, a node drain, a CI job cancelled by a human. A
// row in a lock table, or a lease in a separate lock service, survives that
// death: it stays held until something notices the holder is gone, and until
// then every subsequent deploy either hangs or is told to steal a lock nobody
// can prove is dead.
//
// A session-scoped advisory lock has no such window. The server releases it
// when the connection drops, which the operating system does for a process
// that no longer exists. There is no lease, no heartbeat, no expiry to tune
// and no stealing.
//
// It is also the same failure domain as the thing being protected: if the
// database is unreachable, no migration can run anyway, so a lock that lives
// in the database adds no new way to be unavailable — which a separate lock
// service would (ADR 0055 §D7).
func (m *migrator) acquire(ctx context.Context) (held *advisoryLock, err error) {
	conn, err := m.cfg.db.Conn(ctx)
	//: no connection means no session, so no lock can be held at all.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return nil, failed(MigrationFailed, err, kerrs.String("phase", lockPhase))
	}
	held, err = m.waitForLock(ctx, conn)
	//: a failed acquisition must not leak the connection it was going to
	//: hold: the pool would be one short for the life of the process.
	if err != nil {
		//: the connection is returned to the pool whatever happens; a close
		//: failure is JOINED rather than substituted, because the acquisition
		//: error is the one the caller acts on and hiding either would be a
		//: second defect concealing the first.
		return nil, errors.Join(err, closeVerdict(conn))
	}
	//: the lock is held by THIS connection until release is called.
	return held, nil
}

// waitForLock retries the non-blocking acquisition on the injected clock.
func (m *migrator) waitForLock(
	ctx context.Context, conn *stdsql.Conn,
) (held *advisoryLock, err error) {
	query, arg := tryLockSQL(m.cfg.dialect, m.plan.table)
	release, releaseArg := unlockSQL(m.cfg.dialect, m.plan.table)
	//: the budget is an absolute instant on the injected clock, so a test
	//: asserts the timeout by advancing rather than by sleeping.
	deadline := m.cfg.clk.Now().Add(m.plan.lockBudget)
	//: retry until granted or the budget is spent — the acquisition is
	//: non-blocking, so waiting is this loop's job and not the server's.
	for {
		granted, err := tryOnce(ctx, conn, query, arg)
		//: a rejected lock statement is a real failure, not a busy signal.
		if err != nil {
			//: the driver's error travels beside the verdict.
			return nil, failed(MigrationFailed, err, kerrs.String("phase", lockPhase))
		}
		//: another process is not migrating — this one may.
		if granted {
			//: the release statement is rendered once, up front, so giving
			//: the lock back cannot fail on a formatting error.
			return &advisoryLock{conn: conn, query: release, arg: releaseArg}, nil
		}
		//: somebody else holds it; wait, or give up if the budget is spent.
		if err := m.pause(ctx, deadline); err != nil {
			//: propagate MIGRATION_LOCK_TIMEOUT / the cancellation verdict.
			return nil, err
		}
	}
}

// pause waits one retry interval, or reports why waiting has ended.
func (m *migrator) pause(ctx context.Context, deadline time.Time) error {
	//: the budget is checked BEFORE waiting, so a zero-attempt budget still
	//: reports a timeout rather than sleeping once for nothing.
	if !m.cfg.clk.Now().Before(deadline) {
		//: nothing was applied — that is the fact the caller needs.
		return failed(MigrationLockTimeout, nil)
	}
	select {
	case <-ctx.Done():
		//: the caller gave up; say so with the cancellation beside it.
		return failed(MigrationFailed, ctx.Err(), kerrs.String("phase", lockPhase))
	case <-m.cfg.clk.After(m.plan.retry):
		//: one interval elapsed on the injected clock — try again.
		return nil
	}
}

// tryOnce runs one non-blocking acquisition and reports whether it was
// granted.
func tryOnce(
	ctx context.Context, conn *stdsql.Conn, query string, arg any,
) (granted bool, err error) {
	var answer any
	err = conn.QueryRowContext(ctx, query, arg).Scan(&answer)
	//: a rejected statement is a failure; a false answer is not.
	if err != nil {
		//: the caller turns this into a typed verdict.
		return false, err
	}
	//: the two engines answer in two shapes — see lockGranted.
	return lockGranted(answer), nil
}

// lockGranted reads the truthiness of an advisory-lock answer.
//
// PostgreSQL's pg_try_advisory_lock returns a boolean; MySQL's GET_LOCK
// returns 1, 0, or NULL (NULL meaning the attempt errored). Drivers surface
// those as bool, int64, or — for text protocols — as bytes. Reading all four
// here is not defensive programming, it is the actual shape of the two
// contracts this package supports.
func lockGranted(answer any) bool {
	//: four shapes, because two engines and two protocol encodings is the
	//: actual contract surface — not defensive programming.
	switch value := answer.(type) {
	//: PostgreSQL's pg_try_advisory_lock returns a real boolean.
	case bool:
		//: the server's own answer, unmodified.
		return value
	//: MySQL's GET_LOCK answers 1 or 0 over the binary protocol.
	case int64:
		//: 1 is the grant; 0 is somebody else holding it.
		return value == 1
	//: a text-protocol driver hands the same digit back as raw bytes.
	case []byte:
		//: same contract, undecoded.
		return string(value) == "1"
	//: and as an already-decoded string when the driver converts.
	case string:
		//: same contract, decoded.
		return value == "1"
	//: NULL — which GET_LOCK returns when the attempt ERRORED — or anything
	//: unrecognised.
	default:
		//: not a grant. Refusing is the safe direction: the caller retries or
		//: times out, and never migrates unlocked.
		return false
	}
}

// release gives the lock back and closes the connection that held it.
//
// Both halves are attempted even when the first fails: an unreleased advisory
// lock still dies with the connection, so closing is the guarantee and the
// explicit release is only the courtesy that makes the next run immediate.
func (l *advisoryLock) release(ctx context.Context) error {
	//: courtesy first — the next runner does not have to wait for a TCP
	//: close to propagate. On a DETACHED context, for the reason cleanup
	//: gives: a run abandoned BECAUSE its context ended is exactly the run
	//: whose unlock would otherwise never be sent.
	_, unlockErr := l.conn.ExecContext(cleanup(ctx), l.query, l.arg)
	//: the guarantee — the server drops every session-scoped lock here, so a
	//: failed unlock above costs the NEXT runner a wait and costs this one
	//: nothing. Both are reported side by side so a caller that logs the
	//: return sees a database refusing statements before it becomes an
	//: outage.
	return errors.Join(unlockErr, closeVerdict(l.conn))
}

// closeVerdict returns the connection's close failure, or nil.
//
// A connection returned to the pool is the migration lock's whole guarantee,
// so its failure is worth a sentence rather than a discard — but it is never
// the error a caller acts on, which is why every call site joins it beside the
// outcome instead of returning it alone.
func closeVerdict(conn *stdsql.Conn) error {
	err := conn.Close()
	//: the ordinary path: the session ended and every lock it held is gone.
	if err == nil {
		//: nothing to say.
		return nil
	}
	//: the pool is one connection short; say so with the phase attached.
	return failed(MigrationFailed, err, kerrs.String("phase", lockPhase))
}
