package docstore

import (
	"context"
	"fmt"
	"slices"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// reindexPage is how many documents Reindex reads at a time. A page is read
// whole before its rows are filed, because a transaction runs one statement at
// a time on its connection; the page bounds what that holds in memory.
const reindexPage int = 256

// storedDocument is one row of the documents' table, as Reindex reads it.
type storedDocument struct {
	// key is the store key.
	key []byte
	// raw is the document's JSON.
	raw []byte
}

// Reindex rebuilds every index row from the stored documents, in one
// transaction — a savepoint of the caller's when ctx carries one.
//
// The SQL store keeps its index rows, where the memory and file store rebuild
// theirs at every Open. So a change to the declarations — an index added, a
// key function or IndexKey changed, an index made unique — files nothing for
// the documents already stored until Reindex runs. It drops every row,
// including those of an index no longer declared, reads the documents in key
// order a page at a time, and files each one's keys again.
//
// Documents that break a unique index refuse the rebuild with INDEX_BROKEN,
// naming the index and never the key, and so does a key function that panics;
// a document the type no longer fits is DOCUMENT_UNDECODABLE. Nothing changes
// then. Run it when no write to the store runs beside it: a write in flight
// files its keys under whichever rows it finds.
func (s *SQLStore[T]) Reindex(ctx context.Context) error {
	//: always a transaction: a rebuild is all or nothing.
	return s.run(ctx, true, s.reindex)
}

// reindex is Reindex's work, inside its transaction.
func (s *SQLStore[T]) reindex(ctx context.Context, ex coresql.Executor) error {
	//: every row goes, a removed index's with the rest.
	if _, err := ex.ExecContext(ctx, s.stmts.clearIndex); err != nil {
		//: StatementFailed.
		return s.failed("clear the index rows", err)
	}
	//: pages of documents, in key order, from the first key.
	var after []byte
	for {
		page, err := s.readPage(ctx, ex, after)
		//: StatementFailed.
		if err != nil {
			//: the rebuild rolls back.
			return err
		}
		//: the page's rows, filed unconstrained until every page is in.
		if fileErr := s.refile(ctx, ex, page); fileErr != nil {
			//: DocumentUndecodable, IndexBroken or StatementFailed.
			return fileErr
		}
		//: a short page is the last one.
		if len(page) < reindexPage {
			break
		}
		after = page[len(page)-1].key
	}
	//: the unique indexes checked, then constrained.
	return s.constrainUnique(ctx, ex)
}

// readPage reads up to reindexPage documents stored under keys above after —
// the first page when after is nil.
func (s *SQLStore[T]) readPage(ctx context.Context, ex coresql.Executor, after []byte) (page []storedDocument, err error) {
	query, args := s.stmts.pageAfter, []any{after, int64(reindexPage)}
	//: the first page starts at the first key.
	if after == nil {
		query, args = s.stmts.entriesLimit, []any{int64(reindexPage)}
	}
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed("read the documents to reindex", err)
	}
	//: closed before the page's rows are written: one statement at a time.
	defer func() { err = s.finishRows(rows, "read the documents to reindex", err) }()
	//: one document per row.
	for rows.Next() {
		var doc storedDocument
		//: a row the driver cannot hand over fails the rebuild.
		if scanErr := rows.Scan(&doc.key, &doc.raw); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed("read the documents to reindex", scanErr)
		}
		page = append(page, doc)
	}
	//: the finisher reports a failure that ended the rows early.
	return page, nil
}

// refile files every document of page under its keys, without the unique
// constraint: two documents sharing a unique key are found by
// constrainUnique, which can name the index, rather than by a constraint
// violation, which cannot.
func (s *SQLStore[T]) refile(ctx context.Context, ex coresql.Executor, page []storedDocument) error {
	//: in key order.
	for _, doc := range page {
		v, err := decodeAs[T](s.table, doc.raw)
		//: a document the type no longer fits cannot be indexed.
		if err != nil {
			//: DocumentUndecodable.
			return err
		}
		rows, err := s.keysForRebuild(v)
		//: IndexBroken or KeyTooLong.
		if err != nil {
			//: the rebuild rolls back.
			return err
		}
		//: every row unconstrained for now.
		for i := range rows {
			rows[i].unique = false
		}
		//: a bounded number of rows per statement.
		for batch := range slices.Chunk(rows, indexRowsPerStatement) {
			//: the document's rows.
			if _, execErr := ex.ExecContext(ctx, s.stmts.insertIndexRows(len(batch)), rowArgs(doc.key, batch)...); execErr != nil {
				//: StatementFailed.
				return s.failed("file a document's index rows", execErr)
			}
		}
	}
	//: the page is filed.
	return nil
}

// keysForRebuild computes a stored document's rows, turning a key function's
// panic into INDEX_BROKEN as the file store's rebuild does: stored data an
// index cannot compute keys for is data the index cannot hold.
func (s *SQLStore[T]) keysForRebuild(v T) (rows []indexRow, err error) {
	defer func() {
		recovered := recover()
		//: the ordinary path.
		if recovered == nil {
			return
		}
		//: the value travels as a field, never as the origin.
		rows, err = nil, kerrs.Wrap(coredocstore.IndexBroken, kerrs.WrapParams{},
			kerrs.String("problem", "a key function panicked"), kerrs.String("panic", fmt.Sprint(recovered)))
	}()
	//: the rows a write would file.
	return s.indexRows(v)
}

// constrainUnique refuses the rebuild when a unique index files one key under
// two documents, naming the first such index in declaration order, and
// otherwise marks the unique indexes' rows for the table to constrain.
func (s *SQLStore[T]) constrainUnique(ctx context.Context, ex coresql.Executor) error {
	var names []any
	//: the unique indexes, in declaration order.
	for _, spec := range s.indexes {
		//: only a unique index is constrained.
		if spec.Unique {
			names = append(names, []byte(spec.Name))
		}
	}
	//: nothing to constrain.
	if len(names) == 0 {
		//: rebuilt.
		return nil
	}
	shared, err := s.queryNames(ctx, ex, s.stmts.sharedKeys(len(names)), names)
	//: StatementFailed.
	if err != nil {
		//: the rebuild rolls back.
		return err
	}
	//: in declaration order, so the refusal is the same on every run.
	for _, spec := range s.indexes {
		//: a unique index that does not hold would be a lie every Lookup tells.
		if spec.Unique && slices.Contains(shared, spec.Name) {
			//: IndexBroken, naming the index and never the key.
			return kerrs.Wrap(coredocstore.IndexBroken, kerrs.WrapParams{},
				kerrs.String("problem", "two documents share a unique key"), kerrs.String("store", s.table), kerrs.String("index", spec.Name))
		}
	}
	//: every unique index holds: the table constrains it from now on.
	if _, err := ex.ExecContext(ctx, s.stmts.markUnique(len(names)), names...); err != nil {
		//: StatementFailed.
		return s.failed("constrain the unique indexes", err)
	}
	//: rebuilt.
	return nil
}
