package docstore

import (
	"context"
	stdsql "database/sql"
	"errors"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// tombstoneNum is the number of a trashed document's row in the versions
// table: one no version is given, since versions are numbered from 1.
const tombstoneNum int64 = 0

// Trash moves the document stored under key to the trash, in one
// transaction — a savepoint of the caller's when ctx carries one: its row
// and its index rows go, and a version row numbered 0 — no version's
// number — holds the document and the instant it was trashed beside its
// versions, which stay. It calls OnDelete once that transaction commits.
// DocumentNotFound when no document is stored under key, VersionsNotKept
// when the store keeps no versions: only a store that keeps versions has a
// trash.
func (s *SQLStore[T]) Trash(ctx context.Context, key string) error {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return versionsNotKept(s.table)
	}
	err := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: the document read and locked, removed, and kept in the trash.
		return s.trash(txCtx, ex, key)
	})
	//: nothing trashed.
	if err != nil {
		//: DocumentNotFound, or a failure.
		return err
	}
	s.announce(ctx, &s.onDelete, key)
	//: trashed.
	return nil
}

// Restore brings the document trashed under key back exactly as it was —
// the same JSON, the same current version — files it again and calls
// OnWrite once its transaction commits. DocumentNotTrashed when no
// document is trashed under key, UniqueKeyTaken when another document took
// one of its unique keys meanwhile, DocumentUndecodable when it no longer
// decodes into the store's type; nothing changes on a refusal.
func (s *SQLStore[T]) Restore(ctx context.Context, key string) error {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return versionsNotKept(s.table)
	}
	err := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: the trashed document written and filed again, its row gone.
		return s.restore(txCtx, ex, key)
	})
	//: nothing restored.
	if err != nil {
		//: the refusal, or the classified failure.
		return s.settle(ctx, err)
	}
	s.announce(ctx, &s.onWrite, key)
	//: restored.
	return nil
}

// Purge empties the trash of the document trashed under key: the trashed
// document and every version row of its key go, in one transaction, and a
// document written later under the key starts again at version 1. It calls
// no hook and asks Held nothing, as Delete does not. DocumentNotTrashed
// when no document is trashed under key.
func (s *SQLStore[T]) Purge(ctx context.Context, key string) error {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return versionsNotKept(s.table)
	}
	//: always a transaction: the question and the removal are one.
	return s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: nil, DocumentNotTrashed, or a failure.
		return s.purge(txCtx, ex, key)
	})
}

// Tombstones returns every document in the trash, in key order: its key and
// the instant it was trashed, read in one statement on the transaction ctx
// carries. The statement scans the versions table, as List scans the
// documents'.
func (s *SQLStore[T]) Tombstones(ctx context.Context) (tombstones []coredocstore.TombstoneValue, err error) {
	//: a store that keeps none has no trash.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return nil, versionsNotKept(s.table)
	}
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, s.stmts.tombstones)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("read the trash", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "read the trash", err) }()
	//: in key order.
	for rows.Next() {
		var tomb coredocstore.TombstoneValue
		var at, ns stdsql.NullInt64
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&tomb.Key, &at, &ns); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("read the trash", scanErr)
		}
		//: when it was trashed, to the nanosecond.
		if at.Valid {
			tomb.At = time.Unix(at.Int64, ns.Int64).UTC()
		}
		tombstones = append(tombstones, tomb)
	}
	//: the finisher reports a failure that ended the rows early.
	return tombstones, nil
}

// trash is Trash's work, inside its transaction.
func (s *SQLStore[T]) trash(ctx context.Context, ex coresql.Executor, key string) error {
	var raw []byte
	err := ex.QueryRowContext(ctx, s.stmts.lockDoc, []byte(key)).Scan(&raw)
	//: nothing to trash.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the locking read did not complete.
	if err != nil {
		//: StatementFailed.
		return s.failed("lock a document", err)
	}
	result, execErr := ex.ExecContext(ctx, s.stmts.deleteDoc, []byte(key))
	//: the row is locked, so it is there to be removed.
	if affErr := s.affected(result, execErr, "trash a document"); affErr != nil {
		//: the trash rolls back.
		return affErr
	}
	//: its index rows go with it; a store without indexes has none.
	if len(s.indexes) > 0 {
		//: StatementFailed; the transaction undoes the document's removal.
		if _, err := ex.ExecContext(ctx, s.stmts.deleteIndex, []byte(key)); err != nil {
			//: the trash rolls back.
			return s.failed("delete a document's index rows", err)
		}
	}
	//: the trash's row: the document, and when it was trashed.
	return s.insertVersionRows(ctx, ex, key, []versionRow{{at: new(s.clock.Now().UTC()), doc: raw, num: uint64(tombstoneNum)}})
}

// restore is Restore's work, inside its transaction.
func (s *SQLStore[T]) restore(ctx context.Context, ex coresql.Executor, key string) error {
	num, raw, found, err := s.firstVersion(ctx, ex, key)
	//: StatementFailed.
	if err != nil {
		//: the restoration rolls back.
		return err
	}
	//: no trash row under the key.
	if !found || num != tombstoneNum {
		//: DocumentNotTrashed, naming no key.
		return notTrashed(s.table)
	}
	v, err := decodeAs[T](s.table, raw)
	//: a trashed document the type no longer fits.
	if err != nil {
		//: DocumentUndecodable.
		return err
	}
	p, err := s.prepare(v)
	//: DocumentKeyEmpty, DocumentUnencodable or KeyTooLong.
	if err != nil {
		//: the restoration rolls back.
		return err
	}
	//: the key the document gives is the one it was trashed under.
	if p.key != key {
		//: DocumentKeyChanged.
		return kerrs.Wrap(coredocstore.DocumentKeyChanged, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the document as it was trashed, byte for byte.
	p.raw = raw
	//: DocumentExists — a key written while the store kept no versions —
	//: or a collision.
	if insertErr := s.insertRow(ctx, ex, p); insertErr != nil {
		//: the restoration rolls back.
		return insertErr
	}
	//: its index rows; UniqueKeyTaken when another document took a key.
	if fileErr := s.fileIndexes(ctx, ex, p, false); fileErr != nil {
		//: the restoration rolls back.
		return fileErr
	}
	//: the trash lets it go; its version rows are its versions again.
	if _, execErr := ex.ExecContext(ctx, s.stmts.dropTombstone, []byte(key)); execErr != nil {
		//: StatementFailed.
		return s.failed("empty the trash", execErr)
	}
	//: restored.
	return nil
}

// purge is Purge's work, inside its transaction.
func (s *SQLStore[T]) purge(ctx context.Context, ex coresql.Executor, key string) error {
	num, _, found, err := s.firstVersion(ctx, ex, key)
	//: StatementFailed.
	if err != nil {
		//: the purge rolls back.
		return err
	}
	//: no trash row under the key.
	if !found || num != tombstoneNum {
		//: DocumentNotTrashed, naming no key.
		return notTrashed(s.table)
	}
	//: the trash row and every version row of the key.
	if _, execErr := ex.ExecContext(ctx, s.stmts.dropVersions, []byte(key)); execErr != nil {
		//: StatementFailed.
		return s.failed("purge the trash", execErr)
	}
	//: purged.
	return nil
}

// firstVersion reads the lowest version row of key, locked where the engine
// locks rows: its number and the document it holds, and whether there is
// one. Number 0 is a trashed document's row.
func (s *SQLStore[T]) firstVersion(ctx context.Context, ex coresql.Executor, key string) (num int64, doc []byte, found bool, err error) {
	err = ex.QueryRowContext(ctx, s.stmts.firstVersion, []byte(key)).Scan(&num, &doc)
	//: no version row under the key.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: none.
		return 0, nil, false, nil
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed.
		return 0, nil, false, s.failed("read the versions", err)
	}
	//: the row.
	return num, doc, true, nil
}
