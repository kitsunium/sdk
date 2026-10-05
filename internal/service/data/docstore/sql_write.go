package docstore

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// uniqueKeysPerStatement bounds how many unique keys one unique check asks
// about. At two arguments a key, plus the document's key, it stays under
// SQLite's 999 bound parameters for the same reason indexRowsPerStatement does.
const uniqueKeysPerStatement int = 400

// indexRowsPerStatement bounds how many index rows one INSERT carries. At four
// arguments a row it stays under 999 bound parameters — SQLite's ceiling
// before 3.32 — so a document with many keys is filed in a few statements
// rather than refused by an older engine.
const indexRowsPerStatement int = 200

// conflictKind says what a statement that failed may have collided with.
type conflictKind uint8

// The two collisions a write asks the database about after the fact.
const (
	// docConflict: a document already stored under the key.
	docConflict conflictKind = iota
	// uniqueConflict: a unique index filing one of the keys elsewhere.
	uniqueConflict
)

// mayConflict is a statement failure that may be one of the store's own
// refusals, raced in after the store checked: another transaction inserted
// the same key, or filed the same unique key, and committed first. The write
// rolls back, then asks the database which it was (classify).
type mayConflict struct {
	// cause is the driver's error.
	cause error
	// step names what the write was doing.
	step string
	// written is the document the write was storing: its key and its unique
	// keys are what the database is asked about.
	written *sqlPrepared
	// kind is what the write may have collided with.
	kind conflictKind
}

// Error names the step, and never the driver's words: this value never leaves
// the package — classify turns it into a refusal or into StatementFailed.
func (c *mayConflict) Error() string {
	//: the step, for a log that catches one in flight.
	return "docstore: " + c.step + " failed, and may have collided"
}

// indexRow is one row of the index table: a key a document is filed under.
type indexRow struct {
	// name is the index's name.
	name string
	// key is the index key as the table holds it, transformed by IndexKey.
	key []byte
	// unique marks a unique index's row, which the table refuses twice.
	unique bool
}

// sqlPrepared is a document ready to be written: its key, its JSON and the
// rows it is filed under.
type sqlPrepared struct {
	// key is the store key.
	key string
	// raw is the document's JSON, as it will be stored.
	raw json.RawMessage
	// rows are its index rows, in declaration order.
	rows []indexRow
}

// Put stores v under its key, creating it or replacing whatever was there.
// On a store that keeps versions, it makes one, stamped with nothing.
func (s *SQLStore[T]) Put(ctx context.Context, v T) error {
	//: anything under the key is fine.
	return s.store(ctx, v, upsert, coredocstore.StampValue{})
}

// Insert stores v, which must be new: DocumentExists when its key is taken.
func (s *SQLStore[T]) Insert(ctx context.Context, v T) error {
	//: nothing may be under the key.
	return s.store(ctx, v, insertOnly, coredocstore.StampValue{})
}

// Replace stores v over the document already under its key: DocumentNotFound
// when there is none, so a document deleted meanwhile is never brought back.
func (s *SQLStore[T]) Replace(ctx context.Context, v T) error {
	//: a document must be under the key.
	return s.store(ctx, v, replaceOnly, coredocstore.StampValue{})
}

// Update applies fn to a copy of the document stored under key and stores the
// result, atomically: the document is locked from the read to the write — its
// row on PostgreSQL and MySQL, the database's write lock on SQLite — so no
// other write lands between them. An error from fn changes nothing and is
// returned for errors.Is to find, and a result whose key is not key is
// DocumentKeyChanged. It returns the stored result.
//
// fn runs inside the write's transaction. It may read the store; a read under
// a context carrying no transaction runs on the pool, and so needs a second
// connection. It must not write the document it updates, which is locked by
// the very write that is waiting for fn.
func (s *SQLStore[T]) Update(ctx context.Context, key string, fn func(*T) error) (T, error) {
	//: a new version, stamped with nothing, where versions are kept.
	return s.UpdateStamped(ctx, key, coredocstore.StampValue{}, fn)
}

// UpdateStamped is Update, with what the write says about the version it
// makes.
func (s *SQLStore[T]) UpdateStamped(ctx context.Context, key string, stamp coredocstore.StampValue, fn func(*T) error) (T, error) {
	var zero, result T
	err := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		var err error
		result, err = s.modify(txCtx, ex, key, stamp, fn)
		//: nil, a refusal, fn's own error, or a failure.
		return err
	})
	//: nothing changed.
	if err != nil {
		//: the refusal, fn's error, or the classified failure.
		return zero, s.settle(ctx, err)
	}
	s.announce(ctx, &s.onWrite, key)
	//: the stored result.
	return result, nil
}

// Delete removes the document stored under key, or returns DocumentNotFound.
// Its versions go with it, in the same transaction.
func (s *SQLStore[T]) Delete(ctx context.Context, key string) error {
	err := s.run(ctx, s.several(), func(txCtx context.Context, ex coresql.Executor) error {
		//: the document, then its index rows and its versions.
		return s.remove(txCtx, ex, key)
	})
	//: nothing removed.
	if err != nil {
		//: DocumentNotFound, or a failure.
		return err
	}
	s.announce(ctx, &s.onDelete, key)
	//: removed.
	return nil
}

// store prepares v outside any transaction, writes it, and announces it.
func (s *SQLStore[T]) store(ctx context.Context, v T, mode writeMode, stamp coredocstore.StampValue) error {
	p, err := s.prepare(v)
	//: DocumentKeyEmpty, DocumentUnencodable or KeyTooLong: nothing opened.
	if err != nil {
		//: nothing changed.
		return err
	}
	err = s.run(ctx, s.several(), func(txCtx context.Context, ex coresql.Executor) error {
		existed, old, writeErr := s.writeDocument(txCtx, ex, p, mode)
		//: the document's rows, replacing its previous ones, once its own row
		//: is written; a refusal or a failure rolls the write back.
		if writeErr == nil {
			writeErr = s.fileIndexes(txCtx, ex, p, existed)
		}
		//: its versions, in the same transaction, once nothing refused it.
		if writeErr == nil {
			writeErr = s.keepVersions(txCtx, ex, &storedWrite{meta: stamp.Meta, old: old, raw: p.raw, key: p.key, existed: existed, inPlace: stamp.InPlace})
		}
		//: nil, DocumentExists, DocumentNotFound, a failure, or a collision.
		return writeErr
	})
	//: nothing changed.
	if err != nil {
		//: the refusal, or the classified failure.
		return s.settle(ctx, err)
	}
	s.announce(ctx, &s.onWrite, p.key)
	//: stored.
	return nil
}

// run runs a write's statements where they belong.
//
// Inside the caller's transaction, every write is a savepoint of it, however
// many statements it sends: its failure — a refusal, a collision, a statement
// the database cancelled when a per-call deadline passed — undoes it alone,
// and on PostgreSQL clears the aborted state a failed statement leaves, so the
// caller's transaction stays usable. Outside one, a write of several
// statements runs in a transaction of the store's own, and a write of one
// statement runs on the pool, where one statement is atomic by itself.
func (s *SQLStore[T]) run(ctx context.Context, several bool, work coresql.TxFunc) error {
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
	//: a transaction of the store's own.
	return s.tm.Transact(ctx, s.own, work)
}

// settle turns a failed write's error into what the caller is told: a
// collision is asked about now that the write has rolled back; anything else
// is already the answer.
func (s *SQLStore[T]) settle(ctx context.Context, err error) error {
	conflict, collided := errors.AsType[*mayConflict](err)
	//: a refusal, a failure, or the caller's own error, as it is.
	if !collided {
		//: the answer.
		return err
	}
	//: which refusal, if any, the database now says it was.
	return s.classify(ctx, conflict)
}

// announce tells hooks about key once the transaction that holds the write
// has committed — at once when the write ran in a transaction of its own,
// which has.
func (s *SQLStore[T]) announce(ctx context.Context, hooks *hooks, key string) {
	call := func() { hooks.call(key) }
	//: held until the caller's transaction commits, and dropped with it.
	if s.tx.hold.Defer(ctx, call) {
		//: the transactor calls it.
		return
	}
	//: the write already committed.
	call()
}

// prepare computes everything a write needs from v: its key, its JSON, its
// index rows. It runs the caller's functions, before any transaction opens.
func (s *SQLStore[T]) prepare(v T) (*sqlPrepared, error) {
	key, raw, err := encodeAs(s.table, s.key, v)
	//: DocumentKeyEmpty or DocumentUnencodable.
	if err != nil {
		//: nothing to write.
		return nil, err
	}
	//: a longer key is another key once a column truncates it.
	if len(key) > MaxSQLKeyLen {
		//: KeyTooLong, naming no key.
		return nil, s.tooLong("")
	}
	rows, err := s.indexRows(v)
	//: KeyTooLong.
	if err != nil {
		//: nothing to write.
		return nil, err
	}
	//: ready.
	return &sqlPrepared{key: key, raw: raw, rows: rows}, nil
}

// indexRows computes v's rows in every index, in declaration order: its
// fileable keys, as the table holds them, each once.
func (s *SQLStore[T]) indexRows(v T) ([]indexRow, error) {
	var rows []indexRow
	//: every index, with its own keys.
	for _, spec := range s.indexes {
		//: the rule every engine files by.
		for _, k := range fileableKeys(spec.Keys(v)) {
			filed := s.fileKey(spec.Name, k)
			//: an empty transform files nothing, as an empty key does; two
			//: keys one transform maps together are filed once.
			if len(filed) == 0 || slices.ContainsFunc(rows, func(r indexRow) bool {
				return r.name == spec.Name && bytes.Equal(r.key, filed)
			}) {
				continue
			}
			//: longer than the column.
			if len(filed) > MaxSQLKeyLen {
				//: KeyTooLong, naming the index.
				return nil, s.tooLong(spec.Name)
			}
			rows = append(rows, indexRow{name: spec.Name, key: filed, unique: spec.Unique})
		}
	}
	//: every row to file.
	return rows, nil
}

// several reports whether every write of the store sends more than one
// statement — its index rows, its versions — and so needs a transaction of
// its own outside the caller's.
func (s *SQLStore[T]) several() bool {
	//: a write files index rows, or keeps versions.
	return len(s.indexes) > 0 || s.keep > 0
}

// writeDocument writes a prepared document's row the way mode asks, and
// reports whether a document was already stored under its key — and, on a
// store that keeps versions, the document it replaced.
func (s *SQLStore[T]) writeDocument(
	ctx context.Context, ex coresql.Executor, p *sqlPrepared, mode writeMode,
) (existed bool, old []byte, err error) {
	key := []byte(p.key)
	//: the three write modes.
	switch mode {
	//: create or replace: the engine says which it did.
	case upsert:
		//: a store that keeps versions reads what it replaces first.
		if s.keep > 0 {
			//: whether it replaced, and what.
			return s.claimRow(ctx, ex, p)
		}
		existed, err = s.upsertRow(ctx, ex, key, p.raw)
		//: whether it replaced.
		return existed, nil, err
	//: create only.
	case insertOnly:
		//: never an existing document.
		return false, nil, s.insertRow(ctx, ex, p)
	//: replace only.
	default:
		//: a store that keeps versions reads what it replaces first.
		if s.keep > 0 {
			old, err = s.lockedReplace(ctx, ex, p)
			//: a replacement, and what it replaced.
			return true, old, err
		}
		result, execErr := ex.ExecContext(ctx, s.stmts.replace, []byte(p.raw), key)
		//: DocumentNotFound when no row moved, which is exact: the revision
		//: always changes, so a stored document is always a row affected.
		return true, nil, s.affected(result, execErr, "replace a document")
	}
}

// claimRow creates or replaces a document's row on a store that keeps
// versions, and returns the document it replaced: the claim creates the row,
// or locks the stored one without changing what it holds, and a stored
// document is then written under that lock — so no other writer lands between
// the read and the write, and none creates the key between them either.
func (s *SQLStore[T]) claimRow(ctx context.Context, ex coresql.Executor, p *sqlPrepared) (existed bool, old []byte, err error) {
	key := []byte(p.key)
	//: PostgreSQL and SQLite answer the revision and the row's document.
	if s.stmts.claimReturnsRow() {
		var rev int64
		//: one row: 1 for a creation, the stored document otherwise.
		if scanErr := ex.QueryRowContext(ctx, s.stmts.claim, key, []byte(p.raw)).Scan(&rev, &old); scanErr != nil {
			//: StatementFailed.
			return false, nil, s.failed("write a document", scanErr)
		}
		//: created, with this write's document.
		if rev == 1 {
			//: nothing replaced.
			return false, nil, nil
		}
	} else {
		result, execErr := ex.ExecContext(ctx, s.stmts.claim, key, []byte(p.raw))
		n, countErr := rowsAffected(result, execErr)
		//: MySQL: 1 row affected for a creation, 2 for a row changed.
		if countErr != nil {
			//: StatementFailed.
			return false, nil, s.failed("write a document", countErr)
		}
		//: created, with this write's document.
		if n == 1 {
			//: nothing replaced.
			return false, nil, nil
		}
		//: the stored document, under the lock the claim took.
		if scanErr := ex.QueryRowContext(ctx, s.stmts.lockDoc, key).Scan(&old); scanErr != nil {
			//: StatementFailed.
			return false, nil, s.failed("lock a document", scanErr)
		}
	}
	//: the stored document replaced, under the claim's lock.
	return true, old, s.writeLockedRow(ctx, ex, p)
}

// lockedReplace replaces a stored document on a store that keeps versions,
// and returns the document it replaced: it is read and locked first, as
// Update reads it, and DocumentNotFound when there is none.
func (s *SQLStore[T]) lockedReplace(ctx context.Context, ex coresql.Executor, p *sqlPrepared) (old []byte, err error) {
	err = ex.QueryRowContext(ctx, s.stmts.lockDoc, []byte(p.key)).Scan(&old)
	//: nothing to replace, and nothing is brought back.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return nil, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the locking read did not complete.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("lock a document", err)
	}
	//: replaced, under the lock.
	return old, s.writeLockedRow(ctx, ex, p)
}

// writeLockedRow writes a prepared document over the row its write locked.
func (s *SQLStore[T]) writeLockedRow(ctx context.Context, ex coresql.Executor, p *sqlPrepared) error {
	result, execErr := ex.ExecContext(ctx, s.stmts.writeLocked, []byte(p.raw), []byte(p.key))
	//: the row is locked, so it is there to be written.
	return s.affected(result, execErr, "update a document")
}

// upsertRow creates or replaces a document's row and reports whether it
// replaced one.
func (s *SQLStore[T]) upsertRow(ctx context.Context, ex coresql.Executor, key, raw []byte) (existed bool, err error) {
	args := s.stmts.upsertArgs(key, raw)
	//: PostgreSQL and SQLite return the revision written: 1 for a creation.
	if s.stmts.upsertReturnsRev() {
		var rev int64
		//: one row, the revision.
		if scanErr := ex.QueryRowContext(ctx, s.stmts.upsert, args...).Scan(&rev); scanErr != nil {
			//: StatementFailed.
			return false, s.failed("write a document", scanErr)
		}
		//: a revision above 1 had a previous one.
		return rev > 1, nil
	}
	result, execErr := ex.ExecContext(ctx, s.stmts.upsert, args...)
	//: MySQL: 1 row affected for a creation, 2 for a replacement.
	if execErr != nil {
		//: StatementFailed.
		return false, s.failed("write a document", execErr)
	}
	n, countErr := result.RowsAffected()
	//: a driver that cannot count leaves the question open.
	if countErr != nil {
		//: StatementFailed.
		return false, s.failed("write a document", countErr)
	}
	//: anything but a creation is treated as a replacement, which only costs
	//: a deletion of rows that may not exist.
	return n != 1, nil
}

// insertRow creates a document's row, or refuses a taken key.
func (s *SQLStore[T]) insertRow(ctx context.Context, ex coresql.Executor, p *sqlPrepared) error {
	result, execErr := ex.ExecContext(ctx, s.stmts.insert, []byte(p.key), []byte(p.raw))
	//: PostgreSQL and SQLite answer a taken key with zero rows.
	if s.stmts.insertConflictIsZeroRows() {
		n, err := rowsAffected(result, execErr)
		//: the insertion did not complete.
		if err != nil {
			//: StatementFailed.
			return s.failed("insert a document", err)
		}
		//: the key was taken.
		if n == 0 {
			//: DocumentExists, naming no key.
			return kerrs.Wrap(coredocstore.DocumentExists, kerrs.WrapParams{}, kerrs.String("store", s.table))
		}
		//: created.
		return nil
	}
	//: MySQL answers a taken key with an error, which the rollback lets the
	//: store ask about.
	if execErr != nil {
		//: classified once the write has rolled back.
		return &mayConflict{cause: execErr, step: "insert a document", written: p, kind: docConflict}
	}
	//: created.
	return nil
}

// modify runs Update's read-modify-write inside its transaction.
func (s *SQLStore[T]) modify(ctx context.Context, ex coresql.Executor, key string, stamp coredocstore.StampValue, fn func(*T) error) (T, error) {
	var zero T
	var raw []byte
	err := ex.QueryRowContext(ctx, s.stmts.lockDoc, []byte(key)).Scan(&raw)
	//: nothing to update.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return zero, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the locking read did not complete.
	if err != nil {
		//: StatementFailed.
		return zero, s.failed("lock a document", err)
	}
	v, err := decodeAs[T](s.table, raw)
	//: a stored document the type no longer fits.
	if err != nil {
		//: DocumentUndecodable.
		return zero, err
	}
	//: the caller's change, whose error travels untouched.
	if fnErr := fn(&v); fnErr != nil {
		//: the write rolls back.
		return zero, fnErr
	}
	p, err := s.prepare(v)
	//: DocumentKeyEmpty, DocumentUnencodable or KeyTooLong.
	if err != nil {
		//: the write rolls back.
		return zero, err
	}
	//: an update is not a rename.
	if p.key != key {
		//: DocumentKeyChanged.
		return zero, kerrs.Wrap(coredocstore.DocumentKeyChanged, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the row is locked, so it is there to be written.
	if affErr := s.writeLockedRow(ctx, ex, p); affErr != nil {
		//: StatementFailed, or DocumentNotFound for a row that vanished.
		return zero, affErr
	}
	//: the rows of what it is now.
	if fileErr := s.fileIndexes(ctx, ex, p, true); fileErr != nil {
		//: UniqueKeyTaken, a failure, or a collision.
		return zero, fileErr
	}
	//: its versions, in the same transaction.
	return v, s.keepVersions(ctx, ex, &storedWrite{meta: stamp.Meta, old: raw, raw: p.raw, key: key, existed: true, inPlace: stamp.InPlace})
}

// remove runs Delete's statements.
func (s *SQLStore[T]) remove(ctx context.Context, ex coresql.Executor, key string) error {
	result, execErr := ex.ExecContext(ctx, s.stmts.deleteDoc, []byte(key))
	//: DocumentNotFound when no row went.
	if err := s.affected(result, execErr, "delete a document"); err != nil {
		//: nothing removed.
		return err
	}
	//: its rows go with it; a store without indexes has none.
	if len(s.indexes) > 0 {
		//: StatementFailed; the transaction undoes the document's deletion.
		if _, err := ex.ExecContext(ctx, s.stmts.deleteIndex, []byte(key)); err != nil {
			//: nothing removed.
			return s.failed("delete a document's index rows", err)
		}
	}
	//: its versions go with it; a store that keeps none has none.
	if s.keep > 0 {
		//: StatementFailed; the transaction undoes the document's deletion.
		if _, err := ex.ExecContext(ctx, s.stmts.dropVersions, []byte(key)); err != nil {
			//: nothing removed.
			return s.failed("delete a document's versions", err)
		}
	}
	//: removed, rows, versions and all.
	return nil
}

// fileIndexes replaces a written document's index rows: it refuses a unique
// key another document holds, takes out the rows the document had, and files
// the ones it has now.
func (s *SQLStore[T]) fileIndexes(ctx context.Context, ex coresql.Executor, p *sqlPrepared, existed bool) error {
	//: a store without indexes files nothing.
	if len(s.indexes) == 0 {
		//: nothing to do.
		return nil
	}
	//: a unique key held elsewhere refuses the write before it files anything.
	if err := s.checkUnique(ctx, ex, p, false); err != nil {
		//: UniqueKeyTaken, or a failure.
		return err
	}
	key := []byte(p.key)
	//: a new document has no rows to take out.
	if existed {
		//: the rows of what the document was.
		if _, err := ex.ExecContext(ctx, s.stmts.deleteIndex, key); err != nil {
			//: StatementFailed.
			return s.failed("delete a document's index rows", err)
		}
	}
	//: the rows of what it is now, a bounded number per statement.
	for batch := range slices.Chunk(p.rows, indexRowsPerStatement) {
		//: a unique key raced in since the check collides here.
		if _, err := ex.ExecContext(ctx, s.stmts.insertIndexRows(len(batch)), rowArgs(key, batch)...); err != nil {
			//: classified once the write has rolled back.
			return &mayConflict{cause: err, step: "file a document's index rows", written: p, kind: uniqueConflict}
		}
	}
	//: filed.
	return nil
}

// checkUnique refuses p when a unique index files one of its keys under
// another document, naming the first such index in declaration order. shared
// reads the latest committed rows, which a check after a collision needs. The
// keys are asked about a bounded number at a time, so a document whose unique
// index answers many keys is checked, not refused by the engine's ceiling on
// bound parameters.
func (s *SQLStore[T]) checkUnique(ctx context.Context, ex coresql.Executor, p *sqlPrepared, shared bool) error {
	unique := slices.DeleteFunc(slices.Clone(p.rows), func(r indexRow) bool { return !r.unique })
	var taken []string
	//: one argument list, refilled for each batch.
	args := make([]any, 0, 1+2*min(len(unique), uniqueKeysPerStatement))
	//: every unique key, a bounded batch at a time.
	for batch := range slices.Chunk(unique, uniqueKeysPerStatement) {
		args = append(args[:0], []byte(p.key))
		//: each unique key's index and value.
		for _, row := range batch {
			args = append(args, []byte(row.name), row.key)
		}
		names, err := s.queryNames(ctx, ex, s.stmts.uniqueTaken(len(batch), shared), args)
		//: the check did not complete.
		if err != nil {
			//: StatementFailed.
			return err
		}
		taken = append(taken, names...)
	}
	//: in declaration order, so the refusal names the first index that fails.
	for _, spec := range s.indexes {
		//: held by another document.
		if slices.Contains(taken, spec.Name) {
			//: UniqueKeyTaken, naming the index and never the key.
			return kerrs.Wrap(coredocstore.UniqueKeyTaken, kerrs.WrapParams{}, kerrs.String("store", s.table), kerrs.String("index", spec.Name))
		}
	}
	//: every unique key is free, or the document's own.
	return nil
}

// classify asks the database, once a collided write has rolled back, whether
// the collision was one of the store's refusals. It reads through what ctx
// names — the caller's transaction, usable again after the savepoint's
// rollback, or the pool — past any snapshot (shareLockClause).
func (s *SQLStore[T]) classify(ctx context.Context, conflict *mayConflict) error {
	ex, _ := s.tx.join.Join(ctx)
	//: a document now stored under the key.
	if conflict.kind == docConflict {
		var one int64
		err := ex.QueryRowContext(ctx, s.stmts.exists, []byte(conflict.written.key)).Scan(&one)
		//: the key is taken: the refusal the insertion raced into.
		if err == nil {
			//: DocumentExists, naming no key.
			return kerrs.Wrap(coredocstore.DocumentExists, kerrs.WrapParams{}, kerrs.String("store", s.table))
		}
		//: no document: the failure was something else.
		return s.failed(conflict.step, conflict.cause)
	}
	//: a unique key now held elsewhere.
	if refusal := s.checkUnique(ctx, ex, conflict.written, true); refusal != nil &&
		kerrs.HasCode(refusal, coredocstore.CodeUniqueKeyTaken) {
		//: UniqueKeyTaken, naming the index.
		return refusal
	}
	//: no collision the store knows of: the failure itself.
	return s.failed(conflict.step, conflict.cause)
}

// queryNames runs a query answering one name per row.
func (s *SQLStore[T]) queryNames(ctx context.Context, ex coresql.Executor, query string, args []any) (names []string, err error) {
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("check the unique indexes", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "check the unique indexes", err) }()
	//: one name per row.
	for rows.Next() {
		var name string
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&name); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("check the unique indexes", scanErr)
		}
		names = append(names, name)
	}
	//: the finisher reports a failure that ended the rows early.
	return names, nil
}

// affected turns a write statement's outcome into the write's: a failure, the
// miss no row affected means, or nil.
func (s *SQLStore[T]) affected(result stdsql.Result, execErr error, step string) error {
	n, err := rowsAffected(result, execErr)
	//: the statement did not complete, or its count could not be read.
	if err != nil {
		//: StatementFailed.
		return s.failed(step, err)
	}
	//: no document under the key.
	if n == 0 {
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: a document was written or removed.
	return nil
}

// tooLong is KeyTooLong for the store key, or for a key of index.
func (s *SQLStore[T]) tooLong(index string) error {
	fields := []kerrs.FieldValue{kerrs.String("store", s.table), kerrs.String("limit", strconv.Itoa(MaxSQLKeyLen))}
	//: an index key names its index; the store key names nothing more.
	if index != "" {
		fields = append(fields, kerrs.String("index", index))
	}
	//: never the key.
	return kerrs.Wrap(coredocstore.KeyTooLong, kerrs.WrapParams{}, fields...)
}

// rowsAffected reads a statement's affected-row count, or its failure.
func rowsAffected(result stdsql.Result, execErr error) (int64, error) {
	//: the statement itself failed.
	if execErr != nil {
		//: no count.
		return 0, execErr
	}
	//: the count, or the driver's failure to give one.
	return result.RowsAffected()
}

// rowArgs binds a batch of index rows of the document stored under key.
func rowArgs(key []byte, batch []indexRow) []any {
	args := make([]any, 0, indexRowArgs*len(batch))
	//: index name, index key, document key, and 1 or NULL for uniqueness.
	for _, row := range batch {
		var uniq any
		//: NULLs never collide, so only a unique index's rows are constrained.
		if row.unique {
			uniq = int64(1)
		}
		args = append(args, []byte(row.name), row.key, key, uniq)
	}
	//: in the order insertIndexRows numbers them.
	return args
}
