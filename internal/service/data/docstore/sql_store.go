package docstore

import (
	"context"
	stdsql "database/sql"
	"errors"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// rowsHint is the capacity a read starts its answer with: a guess, grown by
// append past it, and small enough that an empty answer costs little.
const rowsHint int = 16

// rowSet is what finishing a read needs of *sql.Rows: the failure that ended
// the iteration early, and the close.
type rowSet interface {
	// Err returns the failure that ended the iteration, or nil.
	Err() error
	// Close releases the rows.
	Close() error
}

// OpenSQL builds a SQL store from cfg, with the secondary indexes given. It
// sends no statement: its tables are [SQLMigration]'s, run before the store
// is used.
//
//	cases, err := docstore.OpenSQL(
//	    docstore.SQLConfig[Case]{Key: Case.Key, Transactor: tm, Dialect: sql.DialectPostgres, Table: "moderation_intake__cases"},
//	    coredocstore.Unique("reference", func(c Case) string { return c.Reference }),
//	)
func OpenSQL[T any](cfg SQLConfig[T], indexes ...coredocstore.IndexSpec[T]) (*SQLStore[T], error) {
	parts, err := cfg.validate(indexes)
	//: everything decidable without a database is decided here.
	if err != nil {
		//: StoreMisconfigured.
		return nil, err
	}
	store := &SQLStore[T]{
		key:      cfg.Key,
		tm:       cfg.Transactor,
		tx:       parts,
		indexKey: cfg.IndexKey,
		clock:    cfg.Clock,
		held:     cfg.Held,
		byName:   make(map[string]coredocstore.IndexSpec[T], len(indexes)),
		table:    cfg.Table,
		stmts:    renderStatements(cfg.Dialect, cfg.Table),
		indexes:  indexes,
		own:      ownTxOptions(cfg.Dialect),
		keep:     cfg.Versions,
	}
	//: the system clock unless the caller brought one.
	if store.clock == nil {
		store.clock = clock.System
	}
	//: the indexes, by name.
	for _, spec := range indexes {
		store.byName[spec.Name] = spec
	}
	//: ready; no statement was sent.
	return store, nil
}

// ownTxOptions are the options of a transaction the store opens for a write
// of its own, when the caller's context carries none.
//
// READ COMMITTED on MySQL. InnoDB's default, REPEATABLE READ, takes a gap
// lock wherever a locking statement finds no row, and two writers of new
// documents with neighbouring keys then each wait for the other's gap: a
// deadlock, which InnoDB settles by rolling one of them back. A write of the
// store's own needs no snapshot older than its statements, so it asks for none.
// Every other engine keeps its default. Inside the caller's transaction the
// caller's options stand, and ADR 0139 says what REPEATABLE READ costs there.
func ownTxOptions(dialect coresql.Dialect) coresql.TxOptionsValue {
	//: MySQL and MariaDB.
	if dialect == coresql.DialectMySQL {
		//: no gap locks for searches.
		return coresql.TxOptionsValue{Isolation: stdsql.LevelReadCommitted}
	}
	//: the engine's own default.
	return coresql.TxOptionsValue{}
}

// Get returns the document stored under key, or DocumentNotFound.
func (s *SQLStore[T]) Get(ctx context.Context, key string) (T, error) {
	var zero T
	ex, _ := s.tx.join.Join(ctx)
	var raw []byte
	err := ex.QueryRowContext(ctx, s.stmts.getDoc, []byte(key)).Scan(&raw)
	//: no row is the miss; anything else is the database's failure.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming no key.
		return zero, kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table))
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed, the driver's text withheld.
		return zero, s.failed("read a document", err)
	}
	//: a copy of the stored document.
	return decodeAs[T](s.table, raw)
}

// List returns every document, in key order — byte order, as Go compares
// strings.
func (s *SQLStore[T]) List(ctx context.Context) ([]T, error) {
	//: the whole table, one document per row.
	return s.queryDocuments(ctx, "list the documents", s.stmts.listDocs)
}

// Filter returns the documents keep accepts, in key order. It reads every
// document; an index is the way not to.
func (s *SQLStore[T]) Filter(ctx context.Context, keep func(T) bool) ([]T, error) {
	all, err := s.List(ctx)
	//: StatementFailed or DocumentUndecodable.
	if err != nil {
		//: nothing filtered.
		return nil, err
	}
	kept := all[:0]
	//: in key order.
	for _, v := range all {
		//: the caller's predicate.
		if keep(v) {
			kept = append(kept, v)
		}
	}
	//: the accepted documents, never nil.
	return kept, nil
}

// Entries returns up to limit stored documents as JSON, in key order — every
// one when limit is not positive — for a caller that shows documents rather
// than decoding them. Each JSON is the bytes the store wrote.
func (s *SQLStore[T]) Entries(ctx context.Context, limit int) (entries []coredocstore.EntryValue, err error) {
	ex, _ := s.tx.join.Join(ctx)
	query, args := s.stmts.entries, []any(nil)
	//: a positive limit caps the answer in the database.
	if limit > 0 {
		query, args = s.stmts.entriesLimit, []any{int64(limit)}
	}
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("list the entries", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "list the entries", err) }()
	entries = make([]coredocstore.EntryValue, 0, rowsHint)
	//: one entry per row, in key order.
	for rows.Next() {
		var entry coredocstore.EntryValue
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&entry.Key, &entry.JSON); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("list the entries", scanErr)
		}
		entries = append(entries, entry)
	}
	//: the finisher reports a failure that ended the rows early.
	return entries, nil
}

// Count returns how many documents the store holds.
func (s *SQLStore[T]) Count(ctx context.Context) (int, error) {
	ex, _ := s.tx.join.Join(ctx)
	var n int64
	//: one aggregate row.
	if err := ex.QueryRowContext(ctx, s.stmts.count).Scan(&n); err != nil {
		//: StatementFailed.
		return 0, s.failed("count the documents", err)
	}
	//: the count, as an int.
	return int(n), nil
}

// Lookup returns the document holding key in the unique index named index,
// DocumentNotFound when none does, IndexUnknown for an undeclared index and
// IndexNotUnique for an index that may hold several: read that one with Find.
func (s *SQLStore[T]) Lookup(ctx context.Context, index, key string) (T, error) {
	var zero T
	spec, err := s.declared(index)
	//: IndexUnknown.
	if err != nil {
		//: nothing read.
		return zero, err
	}
	//: one document is only meaningful from a unique index.
	if !spec.Unique {
		//: IndexNotUnique, naming the index.
		return zero, kerrs.Wrap(coredocstore.IndexNotUnique, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	indexed := s.fileKey(index, key)
	//: the empty key is filed under no document.
	if len(indexed) == 0 {
		//: DocumentNotFound, naming the index.
		return zero, s.notFoundIn(index)
	}
	ex, _ := s.tx.join.Join(ctx)
	var raw []byte
	err = ex.QueryRowContext(ctx, s.stmts.lookup, []byte(index), indexed).Scan(&raw)
	//: nobody holds the key; it is not quoted, since an index key is often
	//: what a caller must not learn back.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: DocumentNotFound, naming the index.
		return zero, s.notFoundIn(index)
	}
	//: the read did not complete.
	if err != nil {
		//: StatementFailed.
		return zero, s.failed("look a document up", err)
	}
	//: a copy of the holder.
	return decodeAs[T](s.table, raw)
}

// Find returns the documents filed under key in the index named index, in
// store-key order. It reads any index, unique or not, and no document is an
// empty slice, not an error.
func (s *SQLStore[T]) Find(ctx context.Context, index, key string) ([]T, error) {
	//: IndexUnknown.
	if _, err := s.declared(index); err != nil {
		//: nothing read.
		return nil, err
	}
	//: every holder, in store-key order; the empty key binds NULL, which
	//: equals no row, so it finds nobody.
	return s.queryDocuments(ctx, "find documents", s.stmts.find, []byte(index), s.fileKey(index, key))
}

// OnWrite registers fn to be told the key of every document Put, Insert,
// Replace or Update stored, once the transaction that holds the write has
// committed — never for a write that is rolled back — on the goroutine that
// committed it, outside every lock. It returns the function that removes the
// registration.
func (s *SQLStore[T]) OnWrite(fn func(key string)) (remove func()) {
	//: the write hooks.
	return s.onWrite.add(fn)
}

// OnDelete registers fn to be told the key of every document Delete removed,
// on the same terms as OnWrite.
func (s *SQLStore[T]) OnDelete(fn func(key string)) (remove func()) {
	//: the deletion hooks.
	return s.onDelete.add(fn)
}

// queryDocuments runs a query answering one document per row and decodes
// them, in the order the query gave them. It never returns nil for no rows.
func (s *SQLStore[T]) queryDocuments(ctx context.Context, step, query string, args ...any) (docs []T, err error) {
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed(step, err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, step, err) }()
	docs = make([]T, 0, rowsHint)
	//: one document per row.
	for rows.Next() {
		var raw []byte
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&raw); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed(step, scanErr)
		}
		v, decodeErr := decodeAs[T](s.table, raw)
		//: one undecodable document fails the read.
		if decodeErr != nil {
			//: DocumentUndecodable.
			return nil, decodeErr
		}
		docs = append(docs, v)
	}
	//: the finisher reports a failure that ended the rows early.
	return docs, nil
}

// finishRows closes rows and returns the read's verdict: err when the read had
// already failed, else the failure that ended the iteration early — which
// Next reports only as "no more rows" — else the close's.
func (s *SQLStore[T]) finishRows(rows rowSet, step string, err error) error {
	iterErr, closeErr := rows.Err(), rows.Close()
	//: the first failure wins.
	if err != nil {
		//: already failed.
		return err
	}
	//: the iteration's own failure, or the close's.
	if cause := errors.Join(iterErr, closeErr); cause != nil {
		//: StatementFailed.
		return s.failed(step, cause)
	}
	//: every row read.
	return nil
}

// declared returns the index named index, or IndexUnknown.
func (s *SQLStore[T]) declared(index string) (coredocstore.IndexSpec[T], error) {
	spec, found := s.byName[index]
	//: an index the store never declared.
	if !found {
		//: IndexUnknown, naming the index.
		return spec, kerrs.Wrap(coredocstore.IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	//: the declaration.
	return spec, nil
}

// fileKey is key as the index table holds it: transformed by IndexKey when one
// is set, as it is at every write. The empty key is no key, and neither is an
// empty transform.
func (s *SQLStore[T]) fileKey(index, key string) []byte {
	//: the empty key is never filed.
	if key == "" {
		//: no key.
		return nil
	}
	//: kept as it is.
	if s.indexKey == nil {
		//: the key's bytes.
		return []byte(key)
	}
	//: the caller's transform — a keyed hash, typically.
	return s.indexKey(index, key)
}

// notFoundIn is DocumentNotFound for a lookup in index.
func (s *SQLStore[T]) notFoundIn(index string) error {
	//: the table and the index, never the key.
	return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", s.table), kerrs.String("index", index))
}

// failed is StatementFailed for step, with the driver's error beside it and
// its text withheld by service/data/sql's Withheld.
func (s *SQLStore[T]) failed(step string, cause error) error {
	//: the verdict names the table and the step; the cause is reachable, and
	//: silent.
	return errors.Join(
		kerrs.Wrap(coredocstore.StatementFailed, kerrs.WrapParams{}, kerrs.String("store", s.table), kerrs.String("step", step)),
		svcsql.NewWithheld(cause))
}
