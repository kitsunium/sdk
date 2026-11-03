package docstore

import (
	"context"
	stdsql "database/sql"
	"errors"
	"strconv"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// noRevision is the revision a token this engine never gave asks for: a row's
// revision starts at 1, so no row has it.
const noRevision int64 = 0

// Revision returns the document stored under key and its revision — the
// row's rev, which every write moves, in decimal — read in one
// statement on the transaction ctx carries, else on the pool.
// DocumentNotFound when no document is stored under key.
func (s *SQLStore[T]) Revision(ctx context.Context, key string) (T, coredocstore.RevisionValue, error) {
	var zero T
	ex, _ := s.tx.join.Join(ctx)
	var rev int64
	var raw []byte
	err := ex.QueryRowContext(ctx, s.stmts.revision, []byte(key)).Scan(&rev, &raw)
	//: no row is the miss; anything else is the database's failure.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return zero, "", kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed, the driver's text withheld.
		return zero, "", s.failed("read a revision", err)
	}
	v, decodeErr := decodeAs[T](s.table, raw)
	//: a stored document the type no longer fits.
	if decodeErr != nil {
		//: DocumentUndecodable.
		return zero, "", decodeErr
	}
	//: the copy, and the revision of the row it was read from.
	return v, coredocstore.RevisionValue(strconv.FormatInt(rev, 10)), nil
}

// ReplaceIf stores v over the document under its key, as Replace does,
// when the row's revision is still rev: one UPDATE conditioned on it, or
// — on a store that keeps versions, which reads what it replaces — the
// row read and locked on that condition, in the write's transaction.
// When nothing matched, the row is read again past the caller's
// snapshot: RevisionMismatch when it is there, DocumentNotFound when it
// is not. A revision that is not one of this engine's never matches.
func (s *SQLStore[T]) ReplaceIf(ctx context.Context, v T, rev coredocstore.RevisionValue) error {
	p, err := s.prepare(v)
	//: DocumentKeyEmpty, DocumentUnencodable or KeyTooLong: nothing opened.
	if err != nil {
		//: nothing changed.
		return err
	}
	expected, parseErr := strconv.ParseInt(string(rev), 10, 64)
	//: a token another engine, or nobody, gave.
	if parseErr != nil || expected < 1 {
		expected = noRevision
	}
	p.ifRev = expected
	//: written as Replace writes, on the condition.
	return s.write(ctx, p, replaceOnRevision, coredocstore.StampValue{})
}

// replaceOnRevision replaces a stored document on a store that keeps no
// versions, in one statement conditioned on the row's revision.
func (s *SQLStore[T]) replaceOnRevision(ctx context.Context, ex coresql.Executor, p *sqlPrepared) error {
	result, execErr := ex.ExecContext(ctx, s.stmts.replaceIf, []byte(p.raw), []byte(p.key), p.ifRev)
	n, err := rowsAffected(result, execErr)
	//: the statement did not complete, or its count could not be read.
	if err != nil {
		//: StatementFailed.
		return s.failed("replace a document on its revision", err)
	}
	//: the condition failed: which way, the row says.
	if n == 0 {
		//: RevisionMismatch or DocumentNotFound.
		return s.revisionRefused(ctx, ex, p.key)
	}
	//: replaced.
	return nil
}

// lockedReplaceIf replaces a stored document on a store that keeps versions,
// and returns the document it replaced: read and locked on the revision
// first, as lockedReplace reads it, then written under that lock.
func (s *SQLStore[T]) lockedReplaceIf(ctx context.Context, ex coresql.Executor, p *sqlPrepared) (old []byte, err error) {
	err = ex.QueryRowContext(ctx, s.stmts.lockDocIf, []byte(p.key), p.ifRev).Scan(&old)
	//: the condition failed: which way, the row says.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: RevisionMismatch or DocumentNotFound.
		return nil, s.revisionRefused(ctx, ex, p.key)
	}
	//: the locking read did not complete.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("lock a document on its revision", err)
	}
	//: replaced, under the lock.
	return old, s.writeLockedRow(ctx, ex, p)
}

// revisionRefused says why a conditional replacement matched no row: the
// document is there with another revision — RevisionMismatch — or it is not
// there — DocumentNotFound. It asks with the latest committed state (MySQL's
// shared lock reads past a REPEATABLE READ snapshot), as a refused insertion
// does.
func (s *SQLStore[T]) revisionRefused(ctx context.Context, ex coresql.Executor, key string) error {
	var one int64
	err := ex.QueryRowContext(ctx, s.stmts.exists, []byte(key)).Scan(&one)
	//: nothing under the key: never a creation.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed.
		return s.failed("read a revision", err)
	}
	//: another write moved the revision.
	return kerrs.Wrap(coredocstore.RevisionMismatch, kerrs.WrapParams{}, kerrs.String("store", s.table))
}
