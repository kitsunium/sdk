// Package docstore — opening a persistent store: the snapshot read, the
// overlay replayed on top, and the resting state restored.
package docstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// load reads what an earlier store left: the directories created if absent,
// the snapshot, the overlay replayed on top. It writes no data: it reports
// whether Open must fold once the documents are known to be good — when the
// overlay held anything, when there was no snapshot yet, or when the overlay
// directory was just created, since the snapshot's publication flushes the
// directory that holds both, which is what makes a new overlay directory
// itself durable. Open holds the only reference, so no lock is taken.
func (s *Store[T]) load() (needsFold bool, err error) {
	//: the snapshot's own directory, when it has one.
	if dir := path.Dir(s.path); dir != "." {
		//: created 0700 when absent.
		if mkErr := s.fs.MkdirAll(dir, dirPerm); mkErr != nil {
			//: LoadFailed, naming the directory.
			return false, loadFailed(dir, "cannot be created", mkErr)
		}
	}
	_, statErr := fs.Stat(s.fs, s.overlay)
	overlayExisted := statErr == nil
	//: the overlay directory, created 0700 when absent.
	if mkErr := s.fs.MkdirAll(s.overlay, dirPerm); mkErr != nil {
		//: LoadFailed, naming the directory.
		return false, loadFailed(s.overlay, "cannot be created", mkErr)
	}
	snapshotFound, readErr := s.readSnapshot()
	//: LoadFailed.
	if readErr != nil {
		//: no store over a snapshot it could not read.
		return false, readErr
	}
	//: LoadFailed.
	if replayErr := s.readOverlay(); replayErr != nil {
		//: no store over an overlay it could not replay.
		return false, replayErr
	}
	//: at rest already — one snapshot, an empty overlay that is durable — or
	//: not yet.
	return !snapshotFound || !overlayExisted || len(s.pending) > 0, nil
}

// readSnapshot loads the snapshot into docs and reports whether there was
// one. An absent snapshot is an empty store.
func (s *Store[T]) readSnapshot() (found bool, err error) {
	raw, readErr := fs.ReadFile(s.fs, s.path)
	//: a store opened for the first time.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: empty, and no snapshot yet.
		return false, nil
	}
	//: there, and unreadable.
	if readErr != nil {
		//: LoadFailed, naming the file.
		return false, loadFailed(s.path, "unreadable", readErr)
	}
	var object map[string]json.RawMessage
	//: not a JSON object from key to document.
	if decodeErr := json.Unmarshal(raw, &object); decodeErr != nil {
		//: LoadFailed, saying where the JSON broke and never what it held.
		return false, kerrs.Wrap(LoadFailed, kerrs.WrapParams{},
			kerrs.String("file", s.path), kerrs.String("problem", "not a snapshot"), kerrs.String("cause", jsonCause(decodeErr)))
	}
	//: a JSON null decodes without an error into no map at all: a file that
	//: says nothing is not an empty store.
	if object == nil {
		//: LoadFailed, naming the file.
		return false, loadFailed(s.path, "is null, not a snapshot", nil)
	}
	//: every document under its key.
	for key, document := range object {
		//: a document nobody could ask for.
		if key == "" {
			//: LoadFailed, naming the file.
			return false, loadFailed(s.path, "holds a document under the empty key", nil)
		}
		s.docs[key] = document
	}
	//: loaded.
	return true, nil
}

// readOverlay replays every overlay entry over the snapshot. An entry holds a
// key's whole latest state, so the order does not matter. A temporary a
// crashed publication left behind is removed; any other file is left alone.
func (s *Store[T]) readOverlay() error {
	entries, listErr := fs.ReadDir(s.fs, s.overlay)
	//: a directory that cannot be listed cannot be replayed.
	if listErr != nil {
		//: LoadFailed, naming the directory.
		return loadFailed(s.overlay, "unreadable", listErr)
	}
	//: every file in the directory.
	for _, entry := range entries {
		name := entry.Name()
		switch {
		//: one key's latest state.
		case isEntryName(name):
			//: LoadFailed.
			if replayErr := s.replay(name); replayErr != nil {
				//: no store over an entry it could not read.
				return replayErr
			}
		//: a temporary a crashed vfs publication left behind — never data.
		case strings.HasPrefix(name, leftoverPrefix) && strings.HasSuffix(name, leftoverSuffix):
			swallowLeftover(s.fs.Remove(path.Join(s.overlay, name)))
		//: somebody else's file: not the store's to judge or remove.
		default:
		}
	}
	//: replayed.
	return nil
}

// replay applies one overlay entry and marks it pending.
func (s *Store[T]) replay(name string) error {
	file := path.Join(s.overlay, name)
	raw, readErr := fs.ReadFile(s.fs, file)
	//: an entry that cannot be read.
	if readErr != nil {
		//: LoadFailed, naming the file.
		return loadFailed(file, "unreadable", readErr)
	}
	var record entryRecord
	//: not an overlay entry.
	if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
		//: LoadFailed, saying where the JSON broke and never what it held.
		return kerrs.Wrap(LoadFailed, kerrs.WrapParams{},
			kerrs.String("file", file), kerrs.String("problem", "not an overlay entry"), kerrs.String("cause", jsonCause(decodeErr)))
	}
	//: the name is the key's digest: an entry under another key's name was
	//: moved or edited, and replaying it would file it under the wrong key.
	if record.Key == "" || entryName(record.Key) != name {
		//: LoadFailed, naming the file.
		return loadFailed(file, "names another key than its file", nil)
	}
	switch {
	//: the key was deleted after the snapshot.
	case record.Deleted:
		delete(s.docs, record.Key)
	//: an entry that neither holds a document nor records a deletion.
	case len(record.Document) == 0:
		//: LoadFailed, naming the file.
		return loadFailed(file, "holds no document", nil)
	//: the key's latest document.
	default:
		s.docs[record.Key] = record.Document
	}
	s.pending[name] = struct{}{}
	//: replayed.
	return nil
}

// origin names the file a loaded document came from: its overlay entry when
// one was replayed for its key, the snapshot otherwise. The entry's name is a
// digest, so it says nothing about the key.
func (s *Store[T]) origin(key string) string {
	name := entryName(key)
	//: replayed over the snapshot.
	if _, replayed := s.pending[name]; replayed {
		//: the entry.
		return path.Join(s.overlay, name)
	}
	//: as the snapshot held it.
	return s.path
}

// loadFailed is LoadFailed naming a file or directory and its problem, with
// the filesystem's own verdict as a field when there is one.
func loadFailed(file, problem string, cause error) error {
	fields := []kerrs.FieldValue{kerrs.String("file", file), kerrs.String("problem", problem)}
	//: the filesystem's verdict, which names paths and never content.
	if cause != nil {
		fields = append(fields, kerrs.String("cause", cause.Error()))
	}
	//: LoadFailed.
	return kerrs.Wrap(LoadFailed, kerrs.WrapParams{}, fields...)
}

// swallowLeftover discards the failure to remove a crashed publication's
// temporary: it is garbage either way, and a later Open tries again.
func swallowLeftover(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: removed.
		return
	}
	//: still there; harmless, and removed on a later Open.
}
