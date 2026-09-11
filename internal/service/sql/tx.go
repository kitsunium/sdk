// Package sql — hosts the transaction manager: the root transaction, the
// nested savepoint, and the single rule about who is allowed to commit.
package sql

import (
	"context"
	stdsql "database/sql"
	"errors"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// transactor is the concrete core/sql.Transactor. It stays unexported behind
// [NewTransactor] (IFACE-PLUGIN), and its IDENTITY matters: the context chain
// is keyed on the pointer, so a Transact on one database never nests inside a
// transaction of another.
type transactor struct {
	// cfg is the validated, clamped configuration.
	cfg resolved
}

// NewTransactor returns the transaction manager for cfg.DB.
//
// It applies cfg.Pool to the caller's *sql.DB. That is a side effect on a
// value the caller owns, and it is deliberate: a pool policy that has to be
// installed by a second call is a pool policy someone will forget, and the
// consequence of forgetting it (unlimited connections, connections that live
// forever) lands on the database rather than on this process.
func NewTransactor(cfg Config) (manager coresql.Transactor, err error) {
	res, err := cfg.resolve()
	//: a refused Config leaves the caller's DB exactly as it was.
	if err != nil {
		//: propagate CONFIG_INVALID / POOL_MISCONFIGURED unchanged.
		return nil, err
	}
	//: the manager owns nothing but its configuration; the pool is the
	//: caller's and this package never closes it.
	return &transactor{cfg: res}, nil
}

// Transact runs fn inside a transaction, or inside a savepoint of the
// transaction ctx already carries. See the package documentation for what a
// failed nested scope does and does not do to the outer one.
func (t *transactor) Transact(ctx context.Context, opts coresql.TxOptionsValue, fn coresql.TxFunc) error {
	//: a nil unit of work is a wiring fault, refused before anything opens.
	if fn == nil {
		//: the missing field names which half is absent.
		return kerrs.Wrap(ConfigInvalid, kerrs.WrapParams{}, kerrs.String("missing", "fn"))
	}
	//: OUR transaction on this context, if any — another manager's does not
	//: count, because it is another database.
	if scope := scopeFor(ctx, t); scope != nil {
		//: nesting is a savepoint; database/sql has no nested transactions.
		return t.nested(ctx, scope, opts, fn)
	}
	//: no transaction of ours is open — this call owns a real one.
	return t.root(ctx, opts, fn)
}

// root opens a real transaction, runs fn, and is the ONLY thing that commits.
func (t *transactor) root(ctx context.Context, opts coresql.TxOptionsValue, fn coresql.TxFunc) error {
	tx, err := t.cfg.db.BeginTx(ctx, opts.StdOptions())
	//: a transaction the driver would not open ran nothing at all.
	if err != nil {
		//: the driver's error travels beside the verdict.
		return failed(BeginFailed, err)
	}
	state := &txState{tx: tx, dialect: t.cfg.dialect}
	scoped := newScopedExecutor(state)
	settled := false
	defer func() {
		//: the ordinary paths settle the transaction themselves.
		if settled {
			//: nothing left to undo.
			return
		}
		//: a PANIC left the transaction open. Retire the handle and roll
		//: back, then let the panic continue with its original stack — see
		//: the package doc: a panic is not a database outcome, and
		//: converting it here would hide a bug at the site that produced it.
		scoped.retire()
		//: the panic is the news and it is already travelling, so there is no
		//: return value left to report a rollback failure on. It is recorded
		//: on the STATE instead: a unit of work that leaked its executor past
		//: the panic then gets a typed refusal rather than a statement sent
		//: to a connection database/sql has already discarded.
		if rbErr := tx.Rollback(); rbErr != nil && !txEnded(rbErr) {
			//: the transaction's real state is unknown from here.
			state.poison(failed(RollbackFailed, rbErr))
		}
	}()
	err = fn(withScope(ctx, t, state), scoped)
	settled = true
	scoped.retire()
	//: commit, or roll back and report — in exactly one place.
	return t.settle(state, err)
}

// settle ends a root transaction: commit on success, roll back otherwise.
//
// It is the single commit path. A TxFunc cannot reach it, because the
// core/sql.Executor it was handed has no Commit method — which is what makes
// "a callee committed the transaction under its caller" a sentence with no
// spelling (ADR 0055 §D3).
func (t *transactor) settle(state *txState, cause error) error {
	//: a failed unit of work rolls back; the caller's error travels verbatim
	//: so errors.Is still answers across the boundary.
	if cause != nil {
		//: a broken rollback is a SECOND defect, joined, never substituted.
		return errors.Join(cause, rollback(state))
	}
	//: a transaction whose savepoint rollback failed is never committed: the
	//: engine's state is unknown, and committing work the SDK believes it
	//: undid is the one outcome worse than failing.
	if poison := state.poisonedErr(); poison != nil {
		//: report the poison and undo everything.
		return errors.Join(failed(TxPoisoned, poison), rollback(state))
	}
	//: the only COMMIT in this package.
	if err := state.tx.Commit(); err != nil {
		//: says out loud that the unit of work had no effect.
		return failed(CommitFailed, err)
	}
	//: committed.
	return nil
}

// rollback undoes a root transaction and reports only a real failure.
func rollback(state *txState) error {
	err := state.tx.Rollback()
	//: an already-settled transaction is not a failure to report — it is the
	//: normal state after a commit path that returned early.
	if err == nil || errors.Is(err, stdsql.ErrTxDone) {
		//: nothing to say.
		return nil
	}
	//: the driver refused; database/sql discards the connection.
	return failed(RollbackFailed, err)
}

// cleanup returns a context for a statement that must run BECAUSE the
// caller's context ended.
//
// This is ADR 0050's rule, one layer down. A ROLLBACK TO SAVEPOINT issued on
// the very context whose cancellation caused the rollback fails before it
// reaches the driver — (*sql.Tx).grabConn checks ctx.Done() first — so the
// undo the transaction needs is exactly the undo the cancellation prevents.
// Bounding ONE nested scope with its own deadline is an ordinary thing to do,
// and without this detach it would poison the healthy transaction around it.
//
// It drops the deadline as well as the cancellation, and that is stated
// rather than hidden: see the package documentation for what bounds these
// statements instead, and for why this domain does NOT add a second budget
// the way lifecycle does for an arbitrary component's Stop.
func cleanup(ctx context.Context) context.Context {
	//: values (a trace span, a request id) still travel — only the ending
	//: does not.
	return context.WithoutCancel(ctx)
}

// txEnded reports whether err means the transaction is already OVER rather
// than in an unknown state.
//
// The distinction is the whole poison mechanism. Poison means the SDK no
// longer knows what the engine kept. ErrTxDone means database/sql has already
// retired the transaction — every statement was rolled back, nothing can be
// committed, and there is nothing left to be uncertain about. Reading the
// second as the first would turn the ordinary "the caller cancelled" path
// into a diagnosis about savepoints.
func txEnded(err error) bool {
	//: database/sql's own verdict for a transaction that has ended.
	return errors.Is(err, stdsql.ErrTxDone)
}

// nested runs fn inside a SAVEPOINT of the transaction scope already owns.
func (t *transactor) nested(
	ctx context.Context, scope *txScope, opts coresql.TxOptionsValue, fn coresql.TxFunc,
) error {
	//: a savepoint changes neither isolation nor read-only mode. Ignoring the
	//: request would hand the caller a weaker transaction than the one they
	//: asked for, under a nil error.
	if !opts.IsZero() {
		//: refuse, and say what to do instead.
		return kerrs.Wrap(coresql.NestedIsolation, kerrs.WrapParams{})
	}
	//: refuse to open a scope inside a transaction that is already unusable.
	if poison := scope.state.poisonedErr(); poison != nil {
		//: the original cause travels beside the verdict.
		return failed(TxPoisoned, poison)
	}
	name := scope.state.nextSavepoint()
	create, undo, release := savepointSQL(scope.state.dialect, name)
	//: the savepoint itself runs on the transaction, not through a scoped
	//: executor: it is the SDK's own bookkeeping, not the unit of work.
	if _, err := scope.state.tx.ExecContext(ctx, create); err != nil {
		//: the nested scope never started, so the outer one is untouched.
		return failed(SavepointFailed, err,
			kerrs.String("statement", "SAVEPOINT"), kerrs.String("savepoint", name))
	}
	//: run the unit of work, then release or roll back to the savepoint.
	return t.runNested(ctx, scope, fn, savepointStmts{name: name, undo: undo, release: release})
}

// runNested runs the unit of work under an already-created savepoint.
func (t *transactor) runNested(
	ctx context.Context, scope *txScope, fn coresql.TxFunc, sp savepointStmts,
) error {
	scoped := newScopedExecutor(scope.state)
	settled := false
	defer func() {
		//: the ordinary paths settle the savepoint themselves.
		if settled {
			//: nothing left to undo.
			return
		}
		//: a PANIC unwound past the scope. Retire the handle and undo the
		//: savepoint so the outer transaction is not left carrying half of
		//: an abandoned unit of work; the panic then continues.
		scoped.retire()
		//: best-effort, and on a DETACHED context: a panic often coexists
		//: with a cancellation, and the undo must outlive it. A failure here
		//: POISONS, so the outer scope refuses to commit half of an abandoned
		//: unit of work — that is the channel this path reports on, since the
		//: panic itself is already carrying the diagnosis upward.
		if _, undoErr := scope.state.tx.ExecContext(cleanup(ctx), sp.undo); undoErr != nil {
			//: the engine's state is unknown; nothing may be committed.
			scope.state.poison(failed(SavepointFailed, undoErr,
				kerrs.String("statement", "ROLLBACK TO"), kerrs.String("savepoint", sp.name)))
		}
	}()
	err := fn(withScope(ctx, t, scope.state), scoped)
	settled = true
	scoped.retire()
	//: release on success, roll back to the savepoint on failure.
	return t.settleNested(ctx, scope, sp, err)
}

// settleNested ends a nested scope and decides what its outcome does to the
// transaction around it.
func (t *transactor) settleNested(
	ctx context.Context, scope *txScope, sp savepointStmts, cause error,
) error {
	//: the failure path — undo just this scope's work.
	if cause != nil {
		//: the caller's error travels verbatim beside whatever the undo says.
		return errors.Join(cause, t.undoSavepoint(ctx, scope, sp))
	}
	//: success — release the savepoint so a long transaction does not
	//: accumulate one per nested call. On a DETACHED context, because a
	//: scope whose work completed and whose budget then expired still owes
	//: the transaction its RELEASE.
	if _, err := scope.state.tx.ExecContext(cleanup(ctx), sp.release); err != nil {
		//: the whole transaction is already over — there is no savepoint
		//: left to leak and nothing left to commit.
		if txEnded(err) {
			//: the caller's own error, if any, is the only news.
			return nil
		}
		//: a RELEASE that fails leaves a savepoint the SDK believes is gone,
		//: and the transaction's state no longer matches its bookkeeping.
		verdict := failed(SavepointFailed, err,
			kerrs.String("statement", "RELEASE"), kerrs.String("savepoint", sp.name))
		scope.state.poison(verdict)
		//: the outer scope will refuse to commit.
		return verdict
	}
	//: the nested unit of work is folded into the outer transaction, which
	//: still decides whether any of it is committed.
	return nil
}

// undoSavepoint rolls back to the savepoint and decides whether the outer
// transaction survives.
//
// This is the answer to the third question a savepoint implementation owes:
// does a failed sub-transaction condemn the outer one? NO — ROLLBACK TO
// SAVEPOINT is precisely the statement that clears PostgreSQL's aborted state
// (SQLSTATE 25P02), so a caller who catches the inner error has a usable
// transaction to continue in. But if that statement ITSELF fails, the engine's
// state is unknown, and the transaction is poisoned rather than trusted.
func (t *transactor) undoSavepoint(ctx context.Context, scope *txScope, sp savepointStmts) error {
	//: DETACHED: the commonest reason a nested scope fails is that its own
	//: context ended, and the undo must not die of the same cause.
	_, err := scope.state.tx.ExecContext(cleanup(ctx), sp.undo)
	//: two ways this scope ends without news of its own. Either the undo
	//: succeeded — the scope's work is gone, the transaction is clean, and
	//: the caller may catch the error and carry on — or the whole
	//: transaction was retired underneath this scope by a cancellation on an
	//: OUTER context, which database/sql acts on with its own awaitDone
	//: goroutine. Everything is rolled back either way, which is a KNOWN
	//: state and not a poisoning.
	if err == nil || txEnded(err) {
		//: the cause the caller returned is the only news.
		return nil
	}
	//: the recovery statement failed — the SDK no longer knows what the
	//: engine kept, so nothing on this transaction may be committed.
	verdict := failed(SavepointFailed, err,
		kerrs.String("statement", "ROLLBACK TO"), kerrs.String("savepoint", sp.name))
	scope.state.poison(verdict)
	//: joined with the cause by settleNested; the outer commit now refuses.
	return verdict
}
