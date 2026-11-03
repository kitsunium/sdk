package docstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// groupEntryName is the overlay file a batch publishes before its keys'
// entries, and removes after them: every written key's latest state, so a
// crash between leaves a file the next Open replays over the keys' entries.
// It is no entry name — not a key's digest — so a release that predates
// batches leaves it alone, and at most one exists: a batch completes the
// previous one before it starts.
const groupEntryName string = "batch.group.json"

// prepared is a document ready to be stored: its JSON, its keys in every
// index, and its own key.
type prepared struct {
	// raw is the document's JSON.
	raw json.RawMessage
	// keys are its keys in every index, aligned with the store's indexes.
	keys [][]string
	// key is its store key.
	key string
}

// batchOp is one write a batch records: a prepared document and its mode, or
// a deletion of key.
type batchOp struct {
	// p is the prepared document; for a deletion, its key alone.
	p prepared
	// mode is what the write expects to find under its key.
	mode writeMode
	// deleted records a deletion.
	deleted bool
}

// batchWriter records a file-engine batch's writes, each prepared at once,
// outside every lock.
type batchWriter[T any] struct {
	// store prepares each document.
	store *Store[T]
	// ops are the writes, in order.
	ops []batchOp
}

// Put records v, created or replacing whatever is under its key.
func (w *batchWriter[T]) Put(v T) error {
	//: anything under the key is fine.
	return w.record(v, upsert)
}

// Insert records v, which must be new.
func (w *batchWriter[T]) Insert(v T) error {
	//: nothing may be under the key.
	return w.record(v, insertOnly)
}

// Replace records v over the document under its key, which must be there.
func (w *batchWriter[T]) Replace(v T) error {
	//: a document must be under the key.
	return w.record(v, replaceOnly)
}

// Delete records the deletion of the document under key.
func (w *batchWriter[T]) Delete(key string) error {
	w.ops = append(w.ops, batchOp{p: prepared{key: key}, deleted: true})
	//: decided when the batch commits.
	return nil
}

// record prepares v and records its write.
func (w *batchWriter[T]) record(v T, mode writeMode) error {
	p, err := w.store.prepare(v)
	//: DocumentKeyEmpty or DocumentUnencodable, answered at once.
	if err != nil {
		//: nothing recorded.
		return err
	}
	w.ops = append(w.ops, batchOp{p: p, mode: mode})
	//: recorded.
	return nil
}

// batchedKey is one written key's state once the whole batch applied: its
// document or its deletion, its index keys and its versions.
type batchedKey struct {
	// versions are the key's versions after the batch; nil for a deletion.
	versions *versionsRecord
	// key is the store key.
	key string
	// raw is the document; nil for a deletion.
	raw json.RawMessage
	// keys are its index keys, aligned with the store's indexes.
	keys [][]string
	// deleted records a deletion.
	deleted bool
}

// batchPlan is what a batch leaves, every key once, in the order the batch
// first wrote it.
type batchPlan struct {
	// keys are the written keys' final states.
	keys []*batchedKey
}

// Batch writes what fn records as one: planned under the writers' lock
// over the store and the batch's own writes — the first refusal ends it,
// nothing written — then published as ONE group entry holding every
// written key's latest state, then each key's own entry, then the group
// entry removed; a crash leaves the group entry, which the next Open
// replays over the keys' entries. The hooks run once it took effect.
func (s *Store[T]) Batch(fn func(w coredocstore.BatchWriter[T]) error) error {
	w := &batchWriter[T]{store: s}
	//: the caller's own error, returned as it is: nothing was written.
	if err := fn(w); err != nil {
		//: as fn returned it.
		return err
	}
	//: nothing recorded, nothing to write.
	if len(w.ops) == 0 {
		//: an empty batch.
		return nil
	}
	applied, err := s.commitBatch(w.ops)
	//: a batch that took effect is announced, write by write, in order.
	if applied {
		for _, op := range w.ops {
			//: a deletion, or a write.
			if op.deleted {
				s.onDelete.call(op.p.key)
				continue
			}
			s.onWrite.call(op.p.key)
		}
	}
	//: nil, WriteUnconfirmed, or why nothing changed.
	return err
}

// commitBatch plans the batch, makes it durable, applies it, and folds when
// the overlay has grown enough. It reports whether the batch took effect.
func (s *Store[T]) commitBatch(ops []batchOp) (applied bool, err error) {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store takes nothing.
	if s.closed {
		//: StoreClosed.
		return false, kerrs.Wrap(coredocstore.StoreClosed, kerrs.WrapParams{})
	}
	//: a previous batch whose keys' entries are not all written comes first.
	if settleErr := s.settleGroup(); settleErr != nil {
		//: PersistFailed: nothing of this batch changed.
		return false, settleErr
	}
	plan, planErr := s.planBatch(ops)
	//: the first refusal refuses the batch.
	if planErr != nil {
		//: DocumentExists, DocumentNotFound or UniqueKeyTaken.
		return false, planErr
	}
	groupErr := s.publishGroup(plan)
	//: the group entry was not published: nothing changed anywhere.
	if groupErr != nil && !kerrs.HasCode(groupErr, coredocstore.CodeWriteUnconfirmed) {
		//: PersistFailed.
		return false, groupErr
	}
	s.mu.Lock()
	s.applyBatch(plan)
	s.mu.Unlock()
	//: the keys' own entries, then the group entry's removal; what is not
	//: done now is done before the next write publishes anything.
	if completeErr := s.completeGroup(plan); completeErr != nil {
		s.lingering = plan
	}
	s.maybeFold()
	//: applied, and nil or WriteUnconfirmed.
	return true, groupErr
}

// claimKey names a key of a unique index, the i-th of the store's indexes.
type claimKey struct {
	// index is the index's position among the store's.
	index int
	// key is the index key.
	key string
}

// batchView is the store as a batch sees it while it is planned: the store's
// state, under each key the batch already wrote its state instead. The caller
// holds the writers' lock, so the store's maps are read without the read
// lock: nothing else changes them meanwhile.
type batchView[T any] struct {
	// store is what the batch writes over.
	store *Store[T]
	// written maps a key the batch wrote to its state so far.
	written map[string]*batchedKey
	// claimed maps a unique index's key the batch filed to its owner, "" once
	// the batch freed it.
	claimed map[claimKey]string
	// order lists the written keys, first write first.
	order []string
}

// planBatch decides every write of the batch in order over the view, and
// returns the keys' final states, or the first refusal.
func (s *Store[T]) planBatch(ops []batchOp) (*batchPlan, error) {
	view := &batchView[T]{store: s, written: map[string]*batchedKey{}, claimed: map[claimKey]string{}}
	//: in order, each write seeing the ones before it.
	for _, op := range ops {
		//: the refusal names the store and nothing the batch held.
		if err := view.apply(op); err != nil {
			//: the batch is refused whole.
			return nil, err
		}
	}
	plan := &batchPlan{keys: make([]*batchedKey, 0, len(view.order))}
	//: every key once, as the batch leaves it.
	for _, key := range view.order {
		plan.keys = append(plan.keys, view.written[key])
	}
	//: ready to publish.
	return plan, nil
}

// state returns key's document, whether there is one, its versions record and
// its index keys, as the batch sees them.
func (v *batchView[T]) state(key string) (raw json.RawMessage, found bool, record *versionsRecord, keys [][]string) {
	//: the batch's own write first.
	if written, ok := v.written[key]; ok {
		//: as the batch left it.
		return written.raw, !written.deleted, written.versions, written.keys
	}
	s := v.store
	raw, found = s.docs[key]
	keys = make([][]string, len(s.indexes))
	//: the keys the store filed it under.
	for i, ix := range s.indexes {
		keys[i] = ix.owned[key]
	}
	//: as the store holds it.
	return raw, found, s.versions[key], keys
}

// apply decides one write over the view and records its effect.
func (v *batchView[T]) apply(op batchOp) error {
	s, key := v.store, op.p.key
	old, found, record, oldKeys := v.state(key)
	//: a write its mode refuses over what the batch sees.
	if refusal := v.refused(op, found); refusal != nil {
		//: DocumentNotFound or DocumentExists, naming no key.
		return refusal
	}
	v.release(oldKeys)
	//: a deletion takes the document and its versions.
	if op.deleted {
		v.remember(&batchedKey{key: key, deleted: true, keys: make([][]string, len(s.indexes))})
		//: decided.
		return nil
	}
	//: a unique index filing one of the keys elsewhere, the batch's own
	//: earlier writes counted.
	if name, taken := v.taken(key, op.p.keys); taken {
		//: UniqueKeyTaken, naming the index and never the key.
		return kerrs.Wrap(coredocstore.UniqueKeyTaken, kerrs.WrapParams{}, kerrs.String("store", s.path), kerrs.String("index", name))
	}
	versions := s.nextVersionsFrom(key, priorState{raw: old, existed: found, record: record}, op.p.raw, coredocstore.StampValue{})
	v.claim(key, op.p.keys)
	v.remember(&batchedKey{key: key, raw: op.p.raw, keys: op.p.keys, versions: versions})
	//: decided.
	return nil
}

// refused is the error op's mode gives over a document that is there or not
// as found says, nil when the write may go on.
func (v *batchView[T]) refused(op batchOp, found bool) error {
	switch {
	//: a deletion of nothing — a trashed document is not Delete's either —, or
	//: a replacement of a document that is gone.
	case !found && (op.deleted || op.mode == replaceOnly):
		//: DocumentNotFound, naming no key.
		return kerrs.Wrap(coredocstore.DocumentNotFound, kerrs.WrapParams{}, kerrs.String("store", v.store.path))
	//: an insertion over an existing document.
	case found && op.mode == insertOnly && !op.deleted:
		//: DocumentExists, naming no key.
		return kerrs.Wrap(coredocstore.DocumentExists, kerrs.WrapParams{}, kerrs.String("store", v.store.path))
	default:
		//: the mode allows it.
		return nil
	}
}

// remember records a key's state after a write of the batch.
func (v *batchView[T]) remember(state *batchedKey) {
	//: the first write of a key fixes its place.
	if _, seen := v.written[state.key]; !seen {
		v.order = append(v.order, state.key)
	}
	v.written[state.key] = state
}

// release frees, in the view, the unique keys a document held before this
// write.
func (v *batchView[T]) release(keys [][]string) {
	//: every unique index.
	for i, ix := range v.store.indexes {
		//: a non-unique index refuses nothing.
		if !ix.unique {
			continue
		}
		//: each key the owner held, given up.
		for _, k := range keys[i] {
			v.claimed[claimKey{index: i, key: k}] = ""
		}
	}
}

// claim files, in the view, owner's unique keys.
func (v *batchView[T]) claim(owner string, keys [][]string) {
	//: every unique index.
	for i, ix := range v.store.indexes {
		//: a non-unique index refuses nothing.
		if !ix.unique {
			continue
		}
		//: each key, the owner's now.
		for _, k := range keys[i] {
			v.claimed[claimKey{index: i, key: k}] = owner
		}
	}
}

// taken reports the first unique index that refuses owner's keys in the view:
// a key the batch filed to another owner, or one the store files to another
// owner the batch has not freed.
func (v *batchView[T]) taken(owner string, keys [][]string) (name string, taken bool) {
	//: every unique index.
	for i, ix := range v.store.indexes {
		//: a non-unique index refuses nothing.
		if !ix.unique {
			continue
		}
		//: each key the write files.
		for _, k := range keys[i] {
			//: held by another document, in the batch or in the store.
			if v.heldElsewhere(i, ix, owner, k) {
				//: refused.
				return ix.name, true
			}
		}
	}
	//: every unique key free.
	return "", false
}

// heldElsewhere reports whether the unique index ix, the i-th, files k to a
// document other than owner: the one the batch filed it to, or — when the
// batch has not decided — one the store files it to that the batch did not
// rewrite.
func (v *batchView[T]) heldElsewhere(i int, ix *index[T], owner, k string) bool {
	//: the batch decided who holds it.
	if holder, decided := v.claimed[claimKey{index: i, key: k}]; decided {
		//: held by another, or freed.
		return holder != "" && holder != owner
	}
	//: the store's holder, unless the batch rewrote it.
	for other := range ix.entries[k] {
		//: another document that still holds it.
		if _, rewritten := v.written[other]; other != owner && !rewritten {
			//: held elsewhere.
			return true
		}
	}
	//: free.
	return false
}

// publishGroup publishes the group entry: every written key's final state,
// whole or not at all. A memory store has nothing to publish.
func (s *Store[T]) publishGroup(plan *batchPlan) error {
	//: nothing is written in memory.
	if s.fs == nil {
		//: durable as a memory store is.
		return nil
	}
	records := make([]entryRecord, 0, len(plan.keys))
	//: each key's latest state, as its own entry would hold it.
	for _, k := range plan.keys {
		records = append(records, entryRecord{Versions: k.versions, Document: k.raw, Key: k.key, Deleted: k.deleted})
	}
	group, encodeErr := json.Marshal(records)
	//: unreachable while every document is JSON the store encoded.
	if encodeErr != nil {
		//: PersistFailed, naming the step.
		return kerrs.Wrap(coredocstore.PersistFailed, kerrs.WrapParams{},
			kerrs.String("store", s.path), kerrs.String("step", "group"), kerrs.String("cause", encodeCause(encodeErr)))
	}
	//: one publication carries the whole batch.
	return s.publish(path.Join(s.overlay, groupEntryName), group, "group")
}

// applyBatch applies a durable batch to the maps. The caller holds both
// locks.
func (s *Store[T]) applyBatch(plan *batchPlan) {
	//: each key, as the batch leaves it.
	for _, k := range plan.keys {
		//: a deletion, or a write.
		if k.deleted {
			delete(s.docs, k.key)
			s.setVersions(k.key, nil)
			s.unfile(k.key)
		} else {
			s.docs[k.key] = k.raw
			s.setVersions(k.key, k.versions)
			s.file(k.key, k.keys)
		}
		s.markPending(k.key)
	}
	//: the group entry, which the next fold removes if nothing else does.
	if s.fs != nil {
		s.pending[groupEntryName] = struct{}{}
	}
}

// completeGroup publishes each key's own entry, then removes the group
// entry: from then on the batch is as many ordinary entries as it wrote keys.
func (s *Store[T]) completeGroup(plan *batchPlan) error {
	//: nothing is written in memory.
	if s.fs == nil {
		//: complete.
		return nil
	}
	//: each key's entry, the group entry still covering them all.
	for _, k := range plan.keys {
		entryErr := s.persistEntry(k.key, k.raw, k.deleted, k.versions)
		//: a rename that happened is an entry written.
		if entryErr != nil && !kerrs.HasCode(entryErr, coredocstore.CodeWriteUnconfirmed) {
			//: PersistFailed: the group entry stays, and covers them.
			return entryErr
		}
	}
	removeErr := s.fs.Remove(path.Join(s.overlay, groupEntryName))
	//: gone, or already gone.
	if removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		//: PersistFailed: the group entry stays, and still holds what the
		//: keys' entries hold.
		return kerrs.Wrap(coredocstore.PersistFailed, kerrs.WrapParams{},
			kerrs.String("store", s.path), kerrs.String("step", "remove the group entry"), kerrs.String("cause", removeErr.Error()))
	}
	s.mu.Lock()
	delete(s.pending, groupEntryName)
	s.mu.Unlock()
	//: complete.
	return nil
}

// settleGroup completes a batch whose keys' entries were not all written, or
// whose group entry was not removed, before anything else is published: a
// key's entry written while the group entry still held an older state of it
// would be undone by the group entry at the next Open. The caller holds the
// writers' lock.
func (s *Store[T]) settleGroup() error {
	//: nothing outstanding.
	if s.lingering == nil {
		//: free to write.
		return nil
	}
	//: PersistFailed, and the write that asked changes nothing.
	if err := s.completeGroup(s.lingering); err != nil {
		//: still outstanding.
		return err
	}
	s.lingering = nil
	//: settled.
	return nil
}
