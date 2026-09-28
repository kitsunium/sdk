// Package docstore — the SQL store's versions: a third table, written by the
// statements of the write that stores the document, in its transaction
// (ADR 0143).
package docstore

import (
	"bytes"
	"context"
	stdsql "database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// versionRowsPerStatement bounds how many version rows one INSERT carries. At
// five arguments a row it stays under the 999 bound parameters of an SQLite
// older than 3.32, as indexRowsPerStatement does.
const versionRowsPerStatement int = 150

// storedWrite is what a write that stored a document knows of it, for the
// versions it keeps.
type storedWrite struct {
	// meta is what the write's stamp says about the version it makes.
	meta map[string]string
	// old is the document it replaced; nil for a creation.
	old []byte
	// raw is the document it stored.
	raw []byte
	// key is the document's key.
	key string
	// existed says whether it replaced a document.
	existed bool
	// inPlace is the stamp's InPlace: the write makes no version.
	inPlace bool
}

// versionRow is one row of the versions table, as a write inserts it.
type versionRow struct {
	// at is when the write that made the version ran; nil when unknown.
	at *time.Time
	// meta is what that write's caller said about it; nil when nothing.
	meta map[string]string
	// doc is the document the version holds; nil for the current version,
	// whose document is the documents' table's.
	doc []byte
	// num is its number.
	num uint64
}

// PutStamped is Put, with what the write says about the version it makes.
func (s *SQLStore[T]) PutStamped(ctx context.Context, v T, stamp StampValue) error {
	//: anything under the key is fine.
	return s.store(ctx, v, upsert, stamp)
}

// InsertStamped is Insert, with what the write says about the version it
// makes: version 1, whatever stamp.InPlace says.
func (s *SQLStore[T]) InsertStamped(ctx context.Context, v T, stamp StampValue) error {
	//: nothing may be under the key.
	return s.store(ctx, v, insertOnly, stamp)
}

// ReplaceStamped is Replace, with what the write says about the version it
// makes.
func (s *SQLStore[T]) ReplaceStamped(ctx context.Context, v T, stamp StampValue) error {
	//: a document must be under the key.
	return s.store(ctx, v, replaceOnly, stamp)
}

// Versions returns the versions the store keeps of the document stored under
// key, newest first: the current one — the document as it is now — then the
// former ones, read in one statement on the transaction ctx carries.
// DocumentNotFound when no document is stored under key, VersionsNotKept when
// the store keeps no versions.
func (s *SQLStore[T]) Versions(ctx context.Context, key string) (versions []VersionValue, err error) {
	//: a store that keeps none has none to read.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return nil, versionsNotKept(s.table)
	}
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, s.stmts.readVersions, []byte(key))
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("read the versions", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "read the versions", err) }()
	versions = make([]VersionValue, 0, s.keep+1)
	//: newest first; a document stored before versions were kept is one row
	//: without a number.
	for rows.Next() {
		var num, at stdsql.NullInt64
		var meta, doc []byte
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&num, &at, &meta, &doc); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("read the versions", scanErr)
		}
		v, decodeErr := s.versionOf(num, at, meta, doc)
		//: metadata that is not what a write stored.
		if decodeErr != nil {
			//: DocumentUndecodable.
			return nil, decodeErr
		}
		versions = append(versions, v)
	}
	//: no row: no document.
	if len(versions) == 0 {
		//: DocumentNotFound, naming no key.
		return nil, kerrs.Wrap(DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the finisher reports a failure that ended the rows early.
	return versions, nil
}

// Version returns the version numbered number of the document stored under
// key, VersionNotFound when the document keeps none of that number — never
// made, or pruned — and what Versions returns otherwise.
func (s *SQLStore[T]) Version(ctx context.Context, key string, number uint64) (VersionValue, error) {
	all, err := s.Versions(ctx, key)
	//: StatementFailed, DocumentNotFound or VersionsNotKept.
	if err != nil {
		//: nothing read.
		return VersionValue{}, err
	}
	//: the one asked for, or a miss.
	return pickVersion(s.table, all, number)
}

// RewriteVersions rewrites the former versions of the document stored under
// key — every version but the current one, which is the document and which
// only a write changes — in one transaction, a savepoint of the caller's when
// ctx carries one, the document locked as a write locks it. fn receives copies
// of them, newest first, and returns those to keep, in the same order, under
// the rules of [Store].RewriteVersions: VersionsRewriteRefused otherwise, and
// fn's own error returned as it is, nothing changed either way. It calls no
// hook: the document did not change.
func (s *SQLStore[T]) RewriteVersions(ctx context.Context, key string, fn func(former []VersionValue) ([]VersionValue, error)) error {
	//: a store that keeps none has none to rewrite.
	if s.keep == 0 {
		//: VersionsNotKept, naming the store.
		return versionsNotKept(s.table)
	}
	//: always a transaction: a rewrite is all or nothing.
	return s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: nil, DocumentNotFound, fn's own error, a refusal or a failure.
		return s.rewrite(txCtx, ex, key, fn)
	})
}

// rewrite is RewriteVersions' work, inside its transaction.
func (s *SQLStore[T]) rewrite(ctx context.Context, ex coresql.Executor, key string, fn func([]VersionValue) ([]VersionValue, error)) error {
	var raw []byte
	err := ex.QueryRowContext(ctx, s.stmts.lockDoc, []byte(key)).Scan(&raw)
	//: no document, so no version either.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the locking read did not complete.
	if err != nil {
		//: StatementFailed.
		return s.failed("lock a document", err)
	}
	given, err := s.lockedFormers(ctx, ex, key)
	//: StatementFailed or DocumentUndecodable.
	if err != nil {
		//: the rewrite rolls back.
		return err
	}
	copies := make([]VersionValue, len(given))
	//: copies, so fn cannot reach the rows read.
	for i, f := range given {
		copies[i] = VersionValue{At: f.At, Meta: maps.Clone(f.Meta), JSON: slices.Clone(f.Document), Number: f.Number}
	}
	kept, err := fn(copies)
	//: the caller's own error travels untouched.
	if err != nil {
		//: the rewrite rolls back.
		return err
	}
	former, err := checkRewrite(s.table, given, kept)
	//: VersionsRewriteRefused.
	if err != nil {
		//: the rewrite rolls back.
		return err
	}
	//: every former version goes, and those kept come back as rewritten.
	if _, execErr := ex.ExecContext(ctx, s.stmts.dropFormers, []byte(key)); execErr != nil {
		//: StatementFailed.
		return s.failed("rewrite the versions", execErr)
	}
	rows := make([]versionRow, len(former))
	//: in the order fn kept them.
	for i, f := range former {
		rows[i] = versionRow{at: knownInstant(f.At), meta: f.Meta, doc: f.Document, num: f.Number}
	}
	//: nil, or StatementFailed.
	return s.insertVersionRows(ctx, ex, key, rows)
}

// lockedFormers reads the former versions of the document stored under key,
// newest first, locked where the engine locks rows.
func (s *SQLStore[T]) lockedFormers(ctx context.Context, ex coresql.Executor, key string) (former []formerVersion, err error) {
	rows, err := ex.QueryContext(ctx, s.stmts.lockFormers, []byte(key))
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("read the versions", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "read the versions", err) }()
	//: newest first.
	for rows.Next() {
		var num, at stdsql.NullInt64
		var meta, doc []byte
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&num, &at, &meta, &doc); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("read the versions", scanErr)
		}
		v, decodeErr := s.versionOf(num, at, meta, doc)
		//: metadata that is not what a write stored.
		if decodeErr != nil {
			//: DocumentUndecodable.
			return nil, decodeErr
		}
		former = append(former, formerVersion{At: v.At, Meta: v.Meta, Document: v.JSON, Number: v.Number})
	}
	//: the finisher reports a failure that ended the rows early.
	return former, nil
}

// keepVersions records what a write leaves of its document's versions, in
// the write's own transaction: a creation is version 1; a write that changes
// the document makes the next version, the current one becoming the newest
// former one with the document it held; a write stamped InPlace, or storing
// the JSON already stored, makes none. Either way the former versions beyond
// what the store keeps are then pruned, unless Held says the document is
// held.
func (s *SQLStore[T]) keepVersions(ctx context.Context, ex coresql.Executor, w *storedWrite) error {
	//: a store that keeps none touches no versions table.
	if s.keep == 0 {
		//: nothing to record.
		return nil
	}
	now := s.clock.Now().UTC()
	//: a creation is version 1, whatever the stamp says.
	if !w.existed {
		//: its current version.
		return s.insertVersionRows(ctx, ex, w.key, []versionRow{{at: &now, meta: w.meta, num: 1}})
	}
	//: the document keeps its current version: the caller said so, or the
	//: write stores what is already stored.
	if w.inPlace || bytes.Equal(w.old, w.raw) {
		//: pruned all the same, if a hold was lifted or Versions lowered.
		return s.prune(ctx, ex, w.key)
	}
	head, found, err := s.readHead(ctx, ex, w.key)
	//: StatementFailed.
	if err != nil {
		//: the write rolls back.
		return err
	}
	rows := []versionRow{{at: &now, meta: w.meta, num: head + 1}}
	switch {
	//: the current version becomes a former one, with the document it held.
	case found:
		//: StatementFailed; the write rolls back.
		if _, execErr := ex.ExecContext(ctx, s.stmts.retireHead, w.old, []byte(w.key), int64(head)); execErr != nil {
			//: the write rolls back.
			return s.failed("keep a former version", execErr)
		}
	//: a document stored before the store kept versions is version 1, made
	//: at an instant nobody recorded.
	default:
		rows = []versionRow{{doc: w.old, num: 1}, {at: &now, meta: w.meta, num: 2}}
	}
	//: the new current version.
	if insertErr := s.insertVersionRows(ctx, ex, w.key, rows); insertErr != nil {
		//: the write rolls back.
		return insertErr
	}
	//: pruned in this very transaction.
	return s.prune(ctx, ex, w.key)
}

// readHead reads the number of the current version of the document stored
// under key, locked; found is false for a document stored before the store
// kept versions.
func (s *SQLStore[T]) readHead(ctx context.Context, ex coresql.Executor, key string) (head uint64, found bool, err error) {
	var num int64
	err = ex.QueryRowContext(ctx, s.stmts.versionHead, []byte(key)).Scan(&num)
	//: no current version recorded: version 1 is the document itself.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: the first.
		return 1, false, nil
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed.
		return 0, false, s.failed("read the current version", err)
	}
	//: the number, as the table holds it.
	return uint64(num), true, nil
}

// prune removes the former versions of the document stored under key beyond
// what the store keeps — the oldest — unless Held says it is held. It asks
// the database which one is the first to go, and asks Held only when there is
// one.
func (s *SQLStore[T]) prune(ctx context.Context, ex coresql.Executor, key string) error {
	var cut int64
	err := ex.QueryRowContext(ctx, s.stmts.pruneCut, []byte(key), int64(s.keep)).Scan(&cut)
	//: no more former versions than the store keeps.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: nothing to prune.
		return nil
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed.
		return s.failed("prune the versions", err)
	}
	//: a legal hold keeps every version, until a write finds it lifted.
	if s.held != nil && s.held(ctx, key) {
		//: nothing pruned.
		return nil
	}
	//: that one and every older one.
	if _, execErr := ex.ExecContext(ctx, s.stmts.pruneVersions, []byte(key), cut); execErr != nil {
		//: StatementFailed.
		return s.failed("prune the versions", execErr)
	}
	//: pruned.
	return nil
}

// insertVersionRows inserts version rows of the document stored under key, a
// bounded number per statement.
func (s *SQLStore[T]) insertVersionRows(ctx context.Context, ex coresql.Executor, key string, rows []versionRow) error {
	//: a bounded number of rows per statement.
	for batch := range slices.Chunk(rows, versionRowsPerStatement) {
		args, err := s.versionArgs(key, batch)
		//: unreachable: a map of strings always encodes.
		if err != nil {
			//: the write rolls back.
			return err
		}
		//: the batch.
		if _, execErr := ex.ExecContext(ctx, s.stmts.insertVersions(len(batch)), args...); execErr != nil {
			//: StatementFailed.
			return s.failed("keep a version", execErr)
		}
	}
	//: every row inserted.
	return nil
}

// versionArgs binds a batch of version rows of the document stored under
// key: its key, the number, the instant in nanoseconds since 1970 UTC, the
// metadata as a JSON object and the document, NULL where there is none.
func (s *SQLStore[T]) versionArgs(key string, batch []versionRow) ([]any, error) {
	args := make([]any, 0, versionRowArgs*len(batch))
	//: in the order insertVersions numbers them.
	for _, row := range batch {
		var at, meta, doc any
		//: an instant nobody recorded is NULL.
		if row.at != nil {
			at = row.at.UnixNano()
		}
		//: no metadata is NULL.
		if len(row.meta) > 0 {
			encoded, err := json.Marshal(row.meta)
			//: unreachable for a map of strings.
			if err != nil {
				//: DocumentUnencodable, naming what failed.
				return nil, kerrs.Wrap(DocumentUnencodable, kerrs.WrapParams{},
					kerrs.String("store", s.table), kerrs.String("cause", encodeCause(err)))
			}
			meta = encoded
		}
		//: the current version's document is the documents' table's.
		if row.doc != nil {
			doc = row.doc
		}
		args = append(args, []byte(key), int64(row.num), at, meta, doc)
	}
	//: five arguments a row.
	return args, nil
}

// versionOf turns one row of a versions read into a version. A row without a
// number is a document stored before its store kept versions: version 1,
// made at an unknown instant.
func (s *SQLStore[T]) versionOf(num, at stdsql.NullInt64, meta, doc []byte) (VersionValue, error) {
	v := VersionValue{JSON: doc, Number: 1}
	//: a version the table records.
	if num.Valid {
		v.Number = uint64(num.Int64)
	}
	//: when it was made, when anybody recorded it.
	if at.Valid {
		v.At = time.Unix(0, at.Int64).UTC()
	}
	//: what its writer said.
	if len(meta) > 0 {
		//: a JSON object of strings, as a write stored it.
		if err := json.Unmarshal(meta, &v.Meta); err != nil {
			//: DocumentUndecodable, naming what failed and never the value.
			return VersionValue{}, kerrs.Wrap(DocumentUndecodable, kerrs.WrapParams{},
				kerrs.String("store", s.table), kerrs.String("cause", "the metadata of a version: "+jsonCause(err)))
		}
	}
	//: the version.
	return v, nil
}

// knownInstant is at's address, or nil when at is the zero instant — the
// instant of a version nobody recorded.
func knownInstant(at time.Time) *time.Time {
	//: unknown.
	if at.IsZero() {
		//: NULL.
		return nil
	}
	//: known.
	return &at
}
