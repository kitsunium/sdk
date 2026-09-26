// Package docstore — the files: one snapshot at rest, and an overlay of one
// entry per key written since, folded back into the snapshot.
package docstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// filePerm is the mode of every file the store writes: the owner's data, and
// nobody else's to read.
const filePerm fs.FileMode = 0o600

// dirPerm is the mode of every directory the store creates.
const dirPerm fs.FileMode = 0o700

// entrySuffix ends every overlay entry's name.
const entrySuffix string = ".json"

// entryNameLen is an overlay entry's name length: 64 hexadecimal digits of a
// SHA-256 and the suffix.
const entryNameLen int = sha256.Size*2 + len(entrySuffix)

// leftoverPrefix and leftoverSuffix bracket the name of a temporary a crashed
// vfs publication left behind: never a store's data, always removable.
const (
	leftoverPrefix string = ".vfs-"
	leftoverSuffix string = ".tmp"
)

// entryRecord is one overlay entry: the whole latest state of one key since
// the snapshot — its document, or its deletion.
type entryRecord struct {
	// Document is the key's document; absent for a deletion.
	Document json.RawMessage `json:"doc,omitempty"`
	// Key is the store key, which the entry's name only hashes.
	Key string `json:"key"`
	// Deleted records a deletion.
	Deleted bool `json:"deleted,omitempty"`
}

// entryName is the overlay entry's name for key: the SHA-256 of the key, in
// lowercase hexadecimal. It is fixed-length, carries no character a
// filesystem refuses or folds by case, and says nothing about the key.
func entryName(key string) string {
	sum := sha256.Sum256([]byte(key))
	//: the digest names the entry; the record inside names the key.
	return hex.EncodeToString(sum[:]) + entrySuffix
}

// isEntryName reports whether name is an overlay entry's name.
func isEntryName(name string) bool {
	//: the length and the suffix first, cheaply.
	if len(name) != entryNameLen || !strings.HasSuffix(name, entrySuffix) {
		//: not an entry.
		return false
	}
	//: then the digits: lowercase hexadecimal only.
	for _, c := range name[:len(name)-len(entrySuffix)] {
		//: anything else is somebody else's file.
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			//: not an entry.
			return false
		}
	}
	//: an entry.
	return true
}

// markPending records that key's overlay entry is not folded yet. The caller
// holds both locks; a memory store has no overlay.
func (s *Store[T]) markPending(key string) {
	//: nothing is written in memory.
	if s.fs != nil {
		s.pending[entryName(key)] = struct{}{}
	}
}

// persist publishes key's overlay entry — its document, or its deletion —
// before the write is applied. A memory store persists nothing.
func (s *Store[T]) persist(key string, raw json.RawMessage, deleted bool) error {
	//: nothing to persist to.
	if s.fs == nil {
		//: a memory write is done once it is applied.
		return nil
	}
	record, encodeErr := json.Marshal(entryRecord{Document: raw, Key: key, Deleted: deleted})
	//: unreachable while the document is json.Marshal's own output.
	if encodeErr != nil {
		//: PersistFailed, naming the step.
		return kerrs.Wrap(PersistFailed, kerrs.WrapParams{},
			kerrs.String("store", s.path), kerrs.String("step", "entry"), kerrs.String("cause", encodeCause(encodeErr)))
	}
	//: one file, published whole or not at all.
	return s.publish(path.Join(s.overlay, entryName(key)), record, "entry")
}

// publish writes data at name atomically, and says which verdict a failure
// is: PersistFailed when nothing changed, WriteUnconfirmed when the rename
// happened and only the directory flush failed.
func (s *Store[T]) publish(name string, data []byte, step string) error {
	publishErr := s.fs.WriteAtomic(name, data, filePerm)
	//: published and flushed.
	if publishErr == nil {
		//: durable.
		return nil
	}
	fields := []kerrs.FieldValue{
		kerrs.String("store", s.path), kerrs.String("step", step), kerrs.String("cause", publishErr.Error()),
	}
	//: the rename happened: the write took effect, its durability is in doubt.
	if kerrs.HasCode(publishErr, svcvfs.CodeDirectorySyncFailed) {
		//: WriteUnconfirmed.
		return kerrs.Wrap(WriteUnconfirmed, kerrs.WrapParams{}, fields...)
	}
	//: nothing changed.
	return kerrs.Wrap(PersistFailed, kerrs.WrapParams{}, fields...)
}

// maybeFold folds the overlay when the write just made reached the
// threshold. A failure does not fail that write — its own entry is already
// durable — and is kept for Stats until a fold succeeds. The caller holds the
// writers' lock.
func (s *Store[T]) maybeFold() {
	//: nothing to fold in memory, and never on a write when disabled.
	if s.fs == nil || s.foldAt < 0 {
		return
	}
	threshold := s.foldAt
	//: the default rule: as many entries as documents, and at least the floor.
	if threshold == 0 {
		threshold = max(DefaultFoldAt, len(s.docs))
	}
	//: below the threshold, the overlay keeps growing.
	if len(s.pending) < threshold {
		return
	}
	//: fold records its own failure for Stats; the write that reached the
	//: threshold stands either way.
	swallowErr(s.fold())
}

// swallowErr discards an error the caller has already accounted for,
// recording the discard so the error audit reads it as deliberate. Its one
// caller is maybeFold: fold keeps its failure in Stats, and the write that
// triggered it succeeded — its own entry was durable before the fold began.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the failure is in Stats; there is nobody else to hand it to.
}

// Fold rewrites the snapshot with every document and removes the overlay
// entries it now contains. A write folds by itself when the overlay has grown
// enough, and Open and Close fold; Fold is for a caller that wants the
// resting state now — before a backup, say. A memory store has nothing to
// fold.
func (s *Store[T]) Fold() error {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: a closed store folded when it closed.
	if s.closed {
		//: StoreClosed.
		return kerrs.Wrap(StoreClosed, kerrs.WrapParams{})
	}
	//: the fold, under the writers' lock.
	return s.fold()
}

// Close folds the overlay and closes the store: every later call is
// StoreClosed, except Stats and Close, which returns nil. A fold that fails
// leaves its entries where they are — the next Open replays them — and is
// returned; the store closes either way.
func (s *Store[T]) Close() error {
	s.writing.Lock()
	defer s.writing.Unlock()
	//: closing twice is closing once.
	if s.closed {
		//: already closed.
		return nil
	}
	var foldErr error
	//: the resting state: one snapshot, when anything is pending.
	if len(s.pending) > 0 {
		foldErr = s.fold()
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	//: nil, or why the store did not reach its resting state.
	return foldErr
}

// fold rewrites the snapshot from the documents and removes the entries it
// folded. The caller holds the writers' lock, so no entry changes meanwhile:
// an entry removed here holds exactly what the snapshot now holds for its key.
func (s *Store[T]) fold() error {
	//: a memory store has no files.
	if s.fs == nil {
		//: nothing to fold.
		return nil
	}
	snapshot, encodeErr := encodeSnapshot(s.docs)
	//: unreachable while every document is json.Marshal's own output.
	if encodeErr != nil {
		foldErr := kerrs.Wrap(PersistFailed, kerrs.WrapParams{},
			kerrs.String("store", s.path), kerrs.String("step", "snapshot"), kerrs.String("cause", encodeCause(encodeErr)))
		s.recordFold(foldErr, nil)
		//: PersistFailed.
		return foldErr
	}
	//: a snapshot that is not durable folds nothing: the entries stay, and
	//: they are what a restart replays.
	if publishErr := s.publish(s.path, snapshot, "snapshot"); publishErr != nil {
		s.recordFold(publishErr, nil)
		//: PersistFailed or WriteUnconfirmed.
		return publishErr
	}
	removed, removeErr := s.removeFolded()
	s.recordFold(removeErr, removed)
	//: nil, or the entries that are still there to be removed next time.
	return removeErr
}

// removeFolded removes every pending entry, now that the snapshot holds its
// state, and returns the names it removed. An entry it could not remove stays
// pending for the next fold; replaying it meanwhile changes nothing.
func (s *Store[T]) removeFolded() (removed []string, err error) {
	removed = make([]string, 0, len(s.pending))
	var failures []error
	//: every entry the snapshot now contains.
	for name := range s.pending {
		removeErr := s.fs.Remove(path.Join(s.overlay, name))
		//: gone, or already gone.
		if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
			removed = append(removed, name)
			continue
		}
		failures = append(failures, removeErr)
	}
	//: every entry removed.
	if len(failures) == 0 {
		//: the overlay is empty.
		return removed, nil
	}
	//: PersistFailed, counting what is left and quoting the first cause.
	return removed, kerrs.Wrap(PersistFailed, kerrs.WrapParams{},
		kerrs.String("store", s.path), kerrs.String("step", "remove folded entries"),
		kerrs.Int("remaining", len(failures)), kerrs.String("cause", failures[0].Error()))
}

// recordFold records a fold's outcome for Stats: the entries it removed, and
// its failure, nil when it succeeded. A snapshot that could not be written
// removes nothing and counts no fold.
func (s *Store[T]) recordFold(foldErr error, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the entries now inside the snapshot.
	for _, name := range removed {
		delete(s.pending, name)
	}
	//: a fold that got as far as the snapshot counts.
	if removed != nil {
		s.folds++
	}
	s.foldErr = foldErr
}

// encodeSnapshot renders the documents as the snapshot: one JSON object from
// key to document, keys sorted, indented so a person can read the file a
// closed store leaves.
func encodeSnapshot(docs map[string]json.RawMessage) ([]byte, error) {
	//: encoding/json sorts the keys of a map.
	return json.MarshalIndent(docs, "", "  ")
}
