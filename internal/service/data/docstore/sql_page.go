package docstore

import (
	"context"
	"iter"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
)

// Documents yields the documents whose key sorts after after, in key
// order, each with its key and the JSON the store wrote, at most limit of
// them — every one when limit is not positive. It reads them in pages of
// at most a few hundred, each page one statement on the primary key that
// resumes after the last key read, on the transaction ctx carries, else
// on the pool; a page is read whole and its rows closed before the first
// of it is yielded, so the caller may write in the loop. A failed
// statement is yielded once, as StatementFailed, and ends the listing.
func (s *SQLStore[T]) Documents(ctx context.Context, after string, limit int) iter.Seq2[coredocstore.EntryValue, error] {
	//: nothing is read until the range starts.
	return func(yield func(coredocstore.EntryValue, error) bool) {
		pages := pager{limit: limit}
		//: one page after another, each resuming after the last key read.
		for {
			want := pages.next()
			page, err := s.entriesPage(ctx, "page the documents", s.stmts.pageAfter, []byte(after), int64(want))
			//: a stop, a failure, a short page or the limit ends the listing.
			if !yieldEntries(yield, page, err) || !pages.read(len(page), want) {
				//: nothing more.
				return
			}
			after = page[len(page)-1].Key
		}
	}
}

// FindAfter yields the documents filed under key in the index named index
// whose store key sorts after after, in store-key order, at most limit of
// them — every one when limit is not positive —, a page per statement on
// the index table's primary key, decoded as they are yielded.
// IndexUnknown for an undeclared index.
func (s *SQLStore[T]) FindAfter(ctx context.Context, index, key, after string, limit int) iter.Seq2[T, error] {
	//: nothing is read until the range starts.
	return func(yield func(T, error) bool) {
		//: IndexUnknown.
		if _, err := s.declared(index); err != nil {
			yieldFailure(yield, err)
			//: nothing read.
			return
		}
		indexed := s.fileKey(index, key)
		//: the empty key is filed under no document.
		if len(indexed) == 0 {
			//: nothing to find.
			return
		}
		pages := pager{limit: limit}
		//: one page after another, each resuming after the last holder read.
		for {
			want := pages.next()
			page, err := s.entriesPage(ctx, "find a page of documents", s.stmts.findAfter, []byte(index), indexed, []byte(after), int64(want))
			//: a stop, a failure, a short page or the limit ends the listing.
			if !s.yieldPage(yield, page, err) || !pages.read(len(page), want) {
				//: nothing more.
				return
			}
			after = page[len(page)-1].Key
		}
	}
}

// Range yields the documents filed in the ordered index named index under a
// key from lo, included, to hi, excluded — no upper bound when hi is empty
// — in index-key order, then store-key order, at most limit of them —
// every one when limit is not positive —, a page per statement on the
// index table's primary key, which resumes after the last index key and
// store key read; only the documents yielded are read and decoded. The
// bounds reach the table as an index key does, through IndexKey when one
// is set. IndexUnknown for an undeclared index, IndexNotOrdered for one
// declared without Ordered.
func (s *SQLStore[T]) Range(ctx context.Context, index, lo, hi string, limit int) iter.Seq2[T, error] {
	//: nothing is read until the range starts.
	return func(yield func(T, error) bool) {
		spec, found := s.byName[index]
		//: an index the store never declared, or one that keeps no order.
		if !found || !spec.Ordered {
			yieldFailure(yield, notOrdered(index, found))
			//: nothing read.
			return
		}
		cur := rangeCursor{key: bound(s.fileKey(index, lo))}
		query, upper := s.stmts.rangeFrom, []any(nil)
		//: an upper bound, as the table holds it.
		if hi != "" {
			query, upper = s.stmts.rangeBelow, []any{bound(s.fileKey(index, hi))}
		}
		pages := pager{limit: limit}
		//: one page after another, each resuming after the last entry read.
		for {
			want := pages.next()
			page, next, err := s.rangePage(ctx, query, []byte(index), cur, upper, want)
			//: a stop, a failure, a short page or the limit ends the listing.
			if !s.yieldPage(yield, page, err) || !pages.read(len(page), want) {
				//: nothing more.
				return
			}
			cur = next
		}
	}
}

// rangeCursor is where a page of Range resumes: after the entry filed under
// the index key key for the document owner, both as the table holds them.
type rangeCursor struct {
	// key is the last index key read, or the lower bound before the first
	// page.
	key []byte
	// owner is the last document key read, empty before the first page.
	owner string
}

// bound is an index key as a range's bound binds it: never NULL, which
// compares to nothing — the empty key is the lowest there is.
func bound(key []byte) []byte {
	//: no key, or a transform that answered none.
	if key == nil {
		//: the empty bytes, below every key.
		return []byte{}
	}
	//: as the table holds it.
	return key
}

// rangePage reads, in one statement, at most n entries of the ordered index
// above cur, under the bound upper holds when it holds one, and returns them
// with the cursor after the last one. Each entry is a document's key and its
// JSON.
func (s *SQLStore[T]) rangePage(ctx context.Context, query string, index []byte, cur rangeCursor, upper []any, n int) (
	page []coredocstore.EntryValue, next rangeCursor, err error,
) {
	args := append([]any{index, cur.key, cur.key, cur.key, []byte(cur.owner)}, upper...)
	args = append(args, int64(n))
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, cur, s.failed("read a range of an index", err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, "read a range of an index", err) }()
	page, next = make([]coredocstore.EntryValue, 0, min(n, rowsHint)), cur
	//: one entry per row, in index-key order, then store-key order.
	for rows.Next() {
		var key []byte
		var entry coredocstore.EntryValue
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&key, &entry.Key, &entry.JSON); scanErr != nil {
			//: StatementFailed.
			return nil, cur, s.failed("read a range of an index", scanErr)
		}
		page = append(page, entry)
		next = rangeCursor{key: key, owner: entry.Key}
	}
	//: the finisher reports a failure that ended the rows early.
	return page, next, nil
}

// entriesPage runs a query answering a document's key and its JSON per row,
// and returns them in the query's order, its rows closed.
func (s *SQLStore[T]) entriesPage(ctx context.Context, step, query string, args ...any) (page []coredocstore.EntryValue, err error) {
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, query, args...)
	//: the read did not start.
	if err != nil {
		//: StatementFailed.
		return nil, s.failed(step, err)
	}
	//: rows are this call's, so this call closes them.
	defer func() { err = s.finishRows(rows, step, err) }()
	page = make([]coredocstore.EntryValue, 0, rowsHint)
	//: one entry per row.
	for rows.Next() {
		var entry coredocstore.EntryValue
		//: a row the driver cannot hand over fails the read.
		if scanErr := rows.Scan(&entry.Key, &entry.JSON); scanErr != nil {
			//: StatementFailed.
			return nil, s.failed(step, scanErr)
		}
		page = append(page, entry)
	}
	//: the finisher reports a failure that ended the rows early.
	return page, nil
}

// yieldPage decodes a page's documents one at a time and yields each, then
// failure when there is one, and reports whether the caller wants more:
// false once it stopped, after a document that no longer decodes — yielded
// as its failure —, and after a failure, which is a listing's last yield.
func (s *SQLStore[T]) yieldPage(yield func(T, error) bool, page []coredocstore.EntryValue, failure error) bool {
	var zero T
	//: in the page's order, until the caller stops.
	for _, entry := range page {
		v, err := decodeAs[T](s.table, entry.JSON)
		//: DocumentUndecodable ends the listing.
		if err != nil {
			yield(zero, err)
			//: no more.
			return false
		}
		//: the caller stopped.
		if !yield(v, nil) {
			//: no more.
			return false
		}
	}
	//: no failure: the caller may want the next page.
	if failure == nil {
		//: more.
		return true
	}
	yield(zero, failure)
	//: the failure ends the listing, whatever the caller answered.
	return false
}
