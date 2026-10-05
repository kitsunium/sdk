package kit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// localName is the data directory and memory, as a refusal names them.
const localName = "the data directory"

// Transactions (ADR 0004). A transaction belongs to one database: a
// database's own on SQL, kit's own on the data directory and in memory. The
// context carries its innermost level — the outermost kit.Transact, or one
// nested in it, a savepoint —; the store calls under it find it there. What
// leaves the process waits for the outermost commit, and a rollback drops
// it. Nothing of it exists for a product that writes outside one: a store
// call looks the context up, finds nothing, and runs as it always did.

// Transact runs fn in one transaction and returns fn's error: nil commits
// what fn wrote, an error undoes it — a panic too, which then continues.
//
//	err := kit.Transact(ctx, func(ctx context.Context) error {
//		if _, err := Cases.Update(ctx, id, closeCase); err != nil {
//			return err // what this function wrote is undone
//		}
//		if err := Decisions.Insert(ctx, d); err != nil {
//			return err
//		}
//		return Changes.Publish(ctx, Closed{ID: id}) // held: queued after the commit
//	})
//
// A transaction belongs to one database: the one its first store call
// reaches — at its first call on a database, at its first write on the data
// directory, which counts as one, memory with it. Inside it, a store of
// another database is read as usual; writing one is refused ([Invalid],
// [CodeTransactionSpan]). A Transact inside another is a savepoint: its
// failure undoes its own writes, and the outer transaction goes on if its
// caller handles the error.
//
// What leaves the process waits for the outermost commit: a topic's
// publish, a queued command's dispatch, a mail — whose outbox ID [Mailer.Send]
// returns at once —, a watch's notice, the store's write hooks that wake
// workflows and loops, a workflow's OnTransition hooks. Each is checked when
// it is made, so a caller's mistake returns to the caller; a rollback drops
// them, and a savepoint's rollback its own. One a queue refuses after the
// commit is logged and reported, and the commit stands; a process that dies
// between the commit and the release loses what was held.
//
// On a database the transaction is the database's own, at its default
// isolation. On the data directory and in memory kit keeps its own: it takes
// the data's writer turn — one transaction at a time; a write outside any
// waits for it, and such writes share the turn among themselves —, its
// writes land at once, and an error writes each entity's previous value
// back, newest first. It is atomic against an error, not against a crash,
// and a reader may see a write before its commit. A store kept in memory by
// [InMemory] — a cache — is in no transaction: its writes land and stay.
//
// A command's handler runs in one already ([NoTransaction] opts out), and
// so does a workflow's transition.
func Transact(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return Invalid("kit.Transact needs a function to run")
	}
	return transact(ctx, appOf(ctx), fn)
}

// transact runs fn as a level of the transaction ctx carries, or as the
// outermost level of a new one. a is the app, when the caller knows it: a
// transaction learns its app at its first store call otherwise, and draws
// no span.
func transact(ctx context.Context, a *App, fn func(context.Context) error) error {
	ctx, l := beginTransaction(ctx, a)
	defer func() {
		if p := recover(); p != nil {
			if err := l.finish(ctx, errTransactionPanicked); err != nil && !errors.Is(err, errTransactionPanicked) && a != nil {
				logger.Warn(ctx, a.log, "a panicked transaction could not be rolled back", logger.String("error", describeText(err)))
			}
			panic(p)
		}
	}()
	return l.finish(ctx, fn(ctx))
}

// txLevel is a level begun and not yet finished: its unit, and its span
// when it draws one.
type txLevel struct {
	u  *unit
	sp *span
}

// beginTransaction begins a level of the transaction ctx carries, or the
// outermost level of a new one, and returns the context its calls run with:
// in a span of op transaction on the node ctx runs inside, when there is
// one and the app is known. finish ends it.
func beginTransaction(ctx context.Context, a *App) (context.Context, *txLevel) {
	l := &txLevel{u: newUnit(ctx, a)}
	if a != nil && currentNode(ctx) != "" {
		ctx, l.sp = a.begin(ctx, &spanStart{node: currentNode(ctx), op: model.OpTransaction, name: "Transact"})
	}
	return context.WithValue(ctx, unitKey{}, l.u), l
}

// finish ends the level with its function's outcome, draws it, and returns
// the level's own: err, or the database's refusal to commit.
func (l *txLevel) finish(ctx context.Context, err error) error {
	held := l.u.held()
	err = l.u.end(ctx, err)
	l.u.draw(l.sp, err, held)
	return err
}

// held is how many effects that leave the process the level holds.
func (u *unit) held() int {
	u.tx.mu.Lock()
	defer u.tx.mu.Unlock()
	n := 0
	for _, e := range u.tx.held[min(u.heldMark, len(u.tx.held)):] {
		if !e.inside {
			n++
		}
	}
	return n
}

// errTransactionPanicked ends a level whose function panicked: its writes
// are undone as an error's are, and the panic continues.
var errTransactionPanicked = errs.New(CodeTransactionPanic, "TRANSACTION_PANICKED", "the transaction's function panicked",
	"kit: a transaction's function panicked; its writes are undone and the panic continues")

// unitKey carries the innermost level of the transaction a context runs in.
type unitKey struct{}

// unitOf is the level of the transaction ctx runs in, or nil — nil too for
// a transaction that has ended: a context that outlived its transaction
// writes as outside any.
func unitOf(ctx context.Context) *unit {
	u, _ := ctx.Value(unitKey{}).(*unit)
	if u == nil || u.tx.ended() {
		return nil
	}
	return u
}

// withoutUnit is ctx outside any transaction: what a released effect runs
// with.
func withoutUnit(ctx context.Context) context.Context {
	if ctx.Value(unitKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, unitKey{}, (*unit)(nil))
}

// txn is one transaction: its outermost level and every level nested in it.
type txn struct {
	// base is the outermost level's context: a database's transaction
	// begins with it, and ends when it is cancelled.
	base context.Context

	mu  sync.Mutex
	app *App
	// db is the database the transaction belongs to, once a store call
	// reached one; local says it belongs to the data directory and memory,
	// once it wrote there.
	db    *databaseRun
	local bool
	// release gives the local writer turn back, once the transaction took
	// it.
	release func()
	// undo are its local writes, oldest first: what a rollback writes back.
	undo []undoStep
	// held are the effects waiting for the commit, in the order they were
	// made.
	held []heldEffect
	// retold are the keys its writes on a database announced, which a
	// rollback announces again: the workflows and loops over them read
	// them anew.
	retold []func()
	// ends run when the outermost level ends, whatever its outcome, before
	// the effects it held: the data keys' stripes its writes sealed under
	// (seal_store.go).
	ends []func()
	// done is set once the outermost level ended.
	done bool
}

// ended reports whether the transaction's outermost level returned.
func (t *txn) ended() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// unit is one level of a transaction: what its end undoes, and the
// database's savepoint it opened.
type unit struct {
	tx     *txn
	parent *unit
	// the lengths of tx's lists when the level began: its own entries
	// follow them.
	undoMark, heldMark, retoldMark int
	// scope is the level's transaction or savepoint on its database, once
	// a store call on it needed one (transact_sql.go); scopeMu makes one,
	// when calls of the level run at once.
	scopeMu sync.Mutex
	scope   *sqlScope
}

// newUnit is a level of the transaction ctx carries, or the outermost level
// of a new one.
func newUnit(ctx context.Context, a *App) *unit {
	parent := unitOf(ctx)
	if parent == nil {
		return &unit{tx: &txn{base: ctx, app: a}}
	}
	t := parent.tx
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.app == nil {
		t.app = a
	}
	return &unit{tx: t, parent: parent, undoMark: len(t.undo), heldMark: len(t.held), retoldMark: len(t.retold)}
}

// end ends the level with fn's outcome: a savepoint released or rolled back,
// or the transaction committed — its effects then leave — or rolled back —
// they are dropped. It returns the level's outcome: fn's error, or the
// database's refusal to commit.
func (u *unit) end(ctx context.Context, err error) error {
	if u.scope != nil {
		if serr := u.scope.close(err); err == nil {
			err = serr
		}
	}
	t := u.tx
	if err != nil {
		t.rollback(ctx, u)
	}
	if u.parent != nil {
		return err
	}
	t.mu.Lock()
	t.done = true
	release, held, ends := t.release, t.held, t.ends
	t.release, t.held, t.undo, t.retold, t.ends = nil, nil, nil, nil, nil
	t.mu.Unlock()
	if release != nil {
		release()
	}
	for _, fn := range ends {
		fn()
	}
	if err == nil {
		t.let(held)
	}
	return err
}

// rollback undoes what the level wrote on the data directory and in memory,
// newest first, drops the effects it held, and announces again what it wrote
// on a database.
func (t *txn) rollback(ctx context.Context, u *unit) {
	t.mu.Lock()
	undo := t.undo[min(u.undoMark, len(t.undo)):]
	t.undo = t.undo[:min(u.undoMark, len(t.undo))]
	dropped := t.held[min(u.heldMark, len(t.held)):]
	t.held = t.held[:min(u.heldMark, len(t.held))]
	retold := t.retold[min(u.retoldMark, len(t.retold)):]
	t.retold = t.retold[:min(u.retoldMark, len(t.retold))]
	a := t.app
	t.mu.Unlock()
	for _, step := range slices.Backward(undo) {
		if err := step.restore(withoutUnit(context.WithoutCancel(ctx))); err != nil && a != nil {
			logger.Warn(ctx, a.log, "a rolled back write could not be undone", logger.String("store", step.store),
				logger.String("error", describeText(err)))
		}
	}
	for _, e := range dropped {
		if e.drop != nil {
			e.drop()
		}
	}
	for _, retell := range retold {
		retell()
	}
}

// draw ends the level's span: the database it belongs to, its outcome, the
// effects it held, and whether it is a savepoint.
func (u *unit) draw(sp *span, err error, held int) {
	if sp == nil {
		return
	}
	u.tx.drawBackend(sp)
	outcome := model.OutcomeCommit
	if err != nil {
		outcome = model.OutcomeRollback
	}
	sp.attr("outcome", outcome)
	if held > 0 {
		sp.attr("effects", strconv.Itoa(held))
	}
	if u.parent != nil {
		sp.attr("savepoint", "true")
	}
	sp.end(err)
}

// drawBackend says on sp where the transaction writes: its database, the
// data directory's files, or memory.
func (t *txn) drawBackend(sp *span) {
	t.mu.Lock()
	db, local := t.db, t.local
	t.mu.Unlock()
	switch {
	case db != nil:
		sp.attr("database", db.d.name)
		sp.attr("backend", db.d.engineName())
	case local && t.app != nil && t.app.data != nil:
		sp.attr("backend", "file")
	case local:
		sp.attr("backend", "memory")
	}
}

// The one database ------------------------------------------------------

// spanRefusal is the refusal of a write to a store of another database than
// the one the transaction belongs to.
func spanRefusal(store, belongs, other string) error {
	cause := failure(CodeTransactionSpan, "TRANSACTION_SPAN", "a transaction belongs to one database", nil,
		errs.String("store", store), errs.String("database", belongs), errs.String("other", other))
	return Invalid(fmt.Sprintf("%s is kept by %s, and this transaction belongs to %s: a transaction writes one database",
		store, other, belongs)).Wrap(cause)
}

// claimLocal makes the transaction the data directory's, when it belongs to
// no database yet, and takes the data's writer turn for it: at its first
// write there. A transaction that belongs to a database refuses the write.
func (u *unit) claimLocal(ctx context.Context, a *App, store string) error {
	t := u.tx
	t.mu.Lock()
	if t.app == nil {
		t.app = a
	}
	switch {
	case t.db != nil:
		name := t.db.d.name
		t.mu.Unlock()
		return spanRefusal(store, fmt.Sprintf("database %q", name), localName)
	case t.release != nil:
		t.mu.Unlock()
		return nil
	}
	t.local = true
	t.mu.Unlock()
	release, err := a.turn.take(ctx, true)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done || t.release != nil {
		release()
		return nil
	}
	t.release = release
	return nil
}

// Held effects ----------------------------------------------------------

// heldEffect is what leaves the process at the commit: a publish, a mail, a
// notice, a hook. release runs it, once the commit stood; drop, when set,
// cleans up after one a rollback dropped.
type heldEffect struct {
	// node is the node that made it, what a refusal is reported on.
	node    string
	release func() error
	drop    func()
	// inside marks an effect that stays in the process — a store's write
	// hook —: a transaction's span does not count it among what it held.
	inside bool
}

// hold keeps effect until the commit of the transaction ctx carries, and
// reports whether it did: false outside any, where the caller makes it now.
func hold(ctx context.Context, e heldEffect) bool {
	u := unitOf(ctx)
	if u == nil {
		return false
	}
	t := u.tx
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false
	}
	t.held = append(t.held, e)
	return true
}

// atEnd keeps fn for when the transaction ctx carries ends — committed or
// rolled back —, before the effects it held, and reports whether it did:
// false outside any, where the caller runs it now.
func atEnd(ctx context.Context, fn func()) bool {
	u := unitOf(ctx)
	if u == nil {
		return false
	}
	t := u.tx
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false
	}
	t.ends = append(t.ends, fn)
	return true
}

// let releases the effects a committed transaction held, in the order they
// were made. One that fails is logged and reported on its node: the commit
// stands.
func (t *txn) let(held []heldEffect) {
	for _, e := range held {
		if err := letOne(e); err != nil && t.app != nil {
			t.app.problem(e.node, say("transaction.effect", "node", e.node, "detail", describeText(err)))
		}
	}
}

// letOne releases one effect, a panic said as its failure.
func letOne(e heldEffect) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = Unavailable(fmt.Sprintf("an effect held until the commit panicked: %v", clip(fmt.Sprint(p))))
		}
	}()
	return e.release()
}

// retell keeps a function that announces a write on a database again when
// the transaction ctx carries rolls it back.
func retell(ctx context.Context, fn func()) {
	u := unitOf(ctx)
	if u == nil {
		return
	}
	t := u.tx
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.retold = append(t.retold, fn)
	}
}

// The data's local transactions --------------------------------------------

// undoStep writes back what one local write replaced.
type undoStep struct {
	// store names the store, for a log line.
	store   string
	restore func(ctx context.Context) error
}

// recordUndo keeps step, which a rollback of the transaction ctx carries
// runs; nothing outside one.
func recordUndo(ctx context.Context, step undoStep) {
	u := unitOf(ctx)
	if u == nil {
		return
	}
	t := u.tx
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done {
		t.undo = append(t.undo, step)
	}
}

// inLocal reports whether ctx runs in a transaction that holds the data's
// writer turn: a local write then records what it replaces.
func inLocal(ctx context.Context) bool {
	u := unitOf(ctx)
	if u == nil {
		return false
	}
	u.tx.mu.Lock()
	defer u.tx.mu.Unlock()
	return u.tx.release != nil
}
