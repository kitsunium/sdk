package docstore

import (
	"iter"
	"slices"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// documentsPageLen is how many documents one page of Documents reads when
// the caller asks for more, or for every one: enough that a listing of a
// large store is few lock acquisitions — or few statements on SQL —, few
// enough that a page holds a bounded number of documents at once.
const documentsPageLen int = 256

// Documents yields the documents whose key sorts after after, in key
// order, each with its key and a copy of its JSON, at most limit of them —
// every one when limit is not positive — as core/data/docstore.Pager
// says. Each page is taken under the read lock: the next keys above the
// last one yielded, chosen without sorting the store, so a page costs its
// own documents and a pass over the keys, never a copy of them all. A
// write made during the listing is seen when its key sorts after the last
// key read. A closed store yields StoreClosed once.
func (s *Store[T]) Documents(after string, limit int) iter.Seq2[coredocstore.EntryValue, error] {
	//: nothing is read until the range starts.
	return func(yield func(coredocstore.EntryValue, error) bool) {
		pages := pager{limit: limit}
		//: one page after another, each resuming after the last key read.
		for {
			want := pages.next()
			page, err := s.documentsPage(after, want)
			//: a stop, a failure, a short page or the limit ends the listing.
			if !yieldEntries(yield, page, err) || !pages.read(len(page), want) {
				//: nothing more.
				return
			}
			after = page[len(page)-1].Key
		}
	}
}

// pager counts what a paged listing has left to read: limit when it is
// positive, every one otherwise. Both engines' Documents page through it.
type pager struct {
	// limit is the caller's cap, not positive for none.
	limit int
	// done is how many entries were read so far.
	done int
}

// next is the length of the next page: documentsPageLen, or what the
// limit leaves when that is fewer.
func (p *pager) next() int {
	//: no cap, or a cap further than a page.
	if p.limit <= 0 || p.limit-p.done >= documentsPageLen {
		//: a whole page.
		return documentsPageLen
	}
	//: what the cap leaves.
	return p.limit - p.done
}

// read counts a page of got entries read for want, and reports whether
// another page may follow: false after a short page, and once the limit
// is reached.
func (p *pager) read(got, want int) bool {
	p.done += got
	//: a short page is the end; a full one may have a successor.
	return got == want && (p.limit <= 0 || p.done < p.limit)
}

// documentsPage takes, under the read lock, the n documents whose keys
// sort first after after, in key order, each JSON copied once the lock is
// released. A closed store has none, and says StoreClosed.
func (s *Store[T]) documentsPage(after string, n int) ([]coredocstore.EntryValue, error) {
	s.mu.RLock()
	//: a closed store has nothing to hand out.
	if s.closed {
		s.mu.RUnlock()
		//: StoreClosed.
		return nil, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	keys := smallestAfter(s.docs, after, n)
	page := make([]coredocstore.EntryValue, len(keys))
	//: each document's JSON under its key; a stored JSON is never changed
	//: in place, so it is copied outside the lock.
	for i, key := range keys {
		page[i] = coredocstore.EntryValue{Key: key, JSON: s.docs[key]}
	}
	s.mu.RUnlock()
	//: copies, so a caller's edit never reaches the store.
	for i := range page {
		page[i].JSON = slices.Clone(page[i].JSON)
	}
	//: the page, in key order.
	return page, nil
}

// smallestAfter returns the n smallest keys of m that sort after after, in
// order, in one pass over m that keeps at most 2n candidates: a page costs
// a walk of the keys and a sort of twice its length, never a sort of the
// store. n is positive.
func smallestAfter[V any](m map[string]V, after string, n int) []string {
	out := make([]string, 0, 2*n)
	ceiling, bounded := "", false
	//: every key, once.
	for key := range m {
		//: at or below the resume point, or past n smaller ones already held.
		if key <= after || (bounded && key >= ceiling) {
			continue
		}
		out = append(out, key)
		//: full: keep the n smallest, and the largest of them as a bound.
		if len(out) == 2*n {
			slices.Sort(out)
			out = out[:n]
			ceiling, bounded = out[n-1], true
		}
	}
	slices.Sort(out)
	//: at most n.
	if len(out) > n {
		out = out[:n]
	}
	//: the page's keys.
	return out
}

// yieldEntries yields a page's entries in order, then failure when there is
// one, and reports whether the caller wants more: false once it stopped,
// and after a failure, which is a listing's last yield. Both engines'
// Documents yield through it.
func yieldEntries(yield func(coredocstore.EntryValue, error) bool, page []coredocstore.EntryValue, failure error) bool {
	//: in key order, until the caller stops.
	for _, entry := range page {
		//: the caller stopped.
		if !yield(entry, nil) {
			//: no more.
			return false
		}
	}
	//: no failure: the caller may want the next page.
	if failure == nil {
		//: more.
		return true
	}
	yield(coredocstore.EntryValue{}, failure)
	//: the failure ends the listing, whatever the caller answered.
	return false
}

// FindAfter yields the documents filed under key in the index named index
// whose store key sorts after after, in store-key order, at most limit of
// them — every one when limit is not positive —, each decoded as it is
// yielded. The holders are taken under the read lock when the range
// starts. IndexUnknown for an undeclared index, StoreClosed after Close.
func (s *Store[T]) FindAfter(index, key, after string, limit int) iter.Seq2[T, error] {
	//: nothing is read until the range starts.
	return func(yield func(T, error) bool) {
		ix, found := s.byName[index]
		//: an index the store never declared.
		if !found {
			yieldFailure(yield, kerrs.Wrap(coredocstore.IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index)))
			//: nothing read.
			return
		}
		s.mu.RLock()
		owners, closed := ix.holders(key), s.closed
		//: past the resume point.
		start, _ := slices.BinarySearch(owners, after)
		//: the resume point itself was yielded already.
		if start < len(owners) && owners[start] == after {
			start++
		}
		owners = owners[start:]
		//: a positive limit caps the answer.
		if limit > 0 && len(owners) > limit {
			owners = owners[:limit]
		}
		entries := make([]coredocstore.EntryValue, len(owners))
		//: each holder's JSON, taken under the lock.
		for i, owner := range owners {
			entries[i] = coredocstore.EntryValue{Key: owner, JSON: s.docs[owner]}
		}
		s.mu.RUnlock()
		s.yieldDecoded(yield, entries, closed)
	}
}

// Range yields the documents filed in the ordered index named index under a
// key from lo, included, to hi, excluded — no upper bound when hi is empty
// — in index-key order, then store-key order, at most limit of them —
// every one when limit is not positive —, each decoded as it is yielded,
// and no other. The index keeps its entries sorted, so the range is a
// binary search and a walk, taken under the read lock when the range
// starts. IndexUnknown for an undeclared index, IndexNotOrdered for one
// declared without Ordered, StoreClosed after Close.
func (s *Store[T]) Range(index, lo, hi string, limit int) iter.Seq2[T, error] {
	//: nothing is read until the range starts.
	return func(yield func(T, error) bool) {
		ix, found := s.byName[index]
		//: an index the store never declared, or one that keeps no order.
		if !found || ix.order == unordered {
			yieldFailure(yield, notOrdered(index, found))
			//: nothing read.
			return
		}
		s.mu.RLock()
		closed := s.closed
		var entries []coredocstore.EntryValue
		//: an open store's entries in range, each with its holder's JSON.
		if !closed {
			between := ix.between(lo, hi, limit)
			entries = make([]coredocstore.EntryValue, len(between))
			//: in index-key order, then store-key order.
			for i, entry := range between {
				entries[i] = coredocstore.EntryValue{Key: entry.owner, JSON: s.docs[entry.owner]}
			}
		}
		s.mu.RUnlock()
		s.yieldDecoded(yield, entries, closed)
	}
}

// yieldFailure yields err once with the zero value: a listing refused before
// it read anything, whatever the caller answers.
func yieldFailure[V any](yield func(V, error) bool, err error) {
	var zero V
	//: the only yield; there is nothing after it either way.
	if !yield(zero, err) {
		//: the caller stopped.
		return
	}
}

// notOrdered is the refusal of a range read of index: IndexUnknown when the
// store never declared it, IndexNotOrdered when it keeps no order. Both
// engines refuse through it.
func notOrdered(index string, declared bool) error {
	//: an index the store never declared.
	if !declared {
		//: IndexUnknown, naming the index.
		return kerrs.Wrap(coredocstore.IndexUnknown, kerrs.WrapParams{}, kerrs.String("index", index))
	}
	//: IndexNotOrdered, naming the index.
	return kerrs.Wrap(coredocstore.IndexNotOrdered, kerrs.WrapParams{}, kerrs.String("index", index))
}

// yieldDecoded decodes entries one at a time, outside every lock, and
// yields each until the caller stops; a document that no longer decodes is
// yielded once as its failure and ends the listing, and a closed store
// yields StoreClosed once.
func (s *Store[T]) yieldDecoded(yield func(T, error) bool, entries []coredocstore.EntryValue, closed bool) {
	var zero T
	//: a closed store answers nothing.
	if closed {
		yield(zero, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{}))
		//: nothing read.
		return
	}
	//: in the order taken.
	for _, entry := range entries {
		v, err := s.decode(entry.JSON)
		//: DocumentUndecodable ends the listing.
		if err != nil {
			yield(zero, err)
			//: no more.
			return
		}
		//: the caller stopped.
		if !yield(v, nil) {
			//: no more.
			return
		}
	}
}
