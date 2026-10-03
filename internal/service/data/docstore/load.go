// Package docstore — opening a persistent store: the snapshot and the
// versions file read, the overlay replayed on top, and the resting state
// restored.
package docstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
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
	if replayErr := s.replayOverSnapshot(); replayErr != nil {
		//: no store over versions or an overlay it could not trust.
		return false, replayErr
	}
	//: at rest already — one snapshot, an empty overlay that is durable — or
	//: not yet.
	return !snapshotFound || !overlayExisted || len(s.pending) > 0, nil
}

// replayOverSnapshot loads what the snapshot does not hold: the versions
// file, then the overlay replayed over both, then the check that every
// version belongs to a document.
func (s *Store[T]) replayOverSnapshot() error {
	//: LoadFailed.
	if versionsErr := s.readVersions(); versionsErr != nil {
		//: no store over versions it could not read, or does not keep.
		return versionsErr
	}
	//: LoadFailed.
	if replayErr := s.readOverlay(); replayErr != nil {
		//: no store over an overlay it could not replay.
		return replayErr
	}
	//: LoadFailed, or every version belongs to a document.
	return s.versionsWithoutDocument()
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
		return false, kerrs.Wrap(coredocstore.LoadFailed, kerrs.WrapParams{},
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
		return kerrs.Wrap(coredocstore.LoadFailed, kerrs.WrapParams{},
			kerrs.String("file", file), kerrs.String("problem", "not an overlay entry"), kerrs.String("cause", jsonCause(decodeErr)))
	}
	//: the name is the key's digest: an entry under another key's name was
	//: moved or edited, and replaying it would file it under the wrong key.
	if record.Key == "" || entryName(record.Key) != name {
		//: LoadFailed, naming the file.
		return loadFailed(file, "names another key than its file", nil)
	}
	//: versions the store would neither keep nor write back, or that no
	//: store wrote.
	if versionsErr := s.checkEntryVersions(file, record); versionsErr != nil {
		//: LoadFailed, naming the file.
		return versionsErr
	}
	switch {
	//: the key was deleted after the snapshot, its versions with it.
	case record.Deleted:
		delete(s.docs, record.Key)
		s.setVersions(record.Key, nil)
	//: an entry that neither holds a document nor records a deletion.
	case len(record.Document) == 0:
		//: LoadFailed, naming the file.
		return loadFailed(file, "holds no document", nil)
	//: the key's latest document, and its versions.
	default:
		s.docs[record.Key] = record.Document
		s.setVersions(record.Key, compacted(record.Versions))
	}
	s.pending[name] = struct{}{}
	//: replayed.
	return nil
}

// checkEntryVersions refuses, as LoadFailed naming file, the versions an
// overlay entry holds when they are wrong: versions in a store that keeps
// none, versions beside a deletion, which takes them, or versions no store
// wrote.
func (s *Store[T]) checkEntryVersions(file string, record entryRecord) error {
	//: nothing recorded, which is always fine.
	if record.Versions == nil {
		//: nothing to refuse.
		return nil
	}
	//: the store would drop them at its next fold.
	if s.keep == 0 {
		//: the configuration, not the file, is what to change.
		return loadFailed(file, "keeps versions, and the store keeps none: open it with Versions", nil)
	}
	//: a deletion takes a document's versions with it.
	if record.Deleted {
		//: not an entry a store wrote.
		return loadFailed(file, "records a deletion and keeps versions", nil)
	}
	//: numbered from 1, newest first, each holding a document.
	return checkRecord(file, record.Versions)
}

// readVersions loads the versions file into versions when the store keeps
// versions. A store that keeps none refuses one: it would neither maintain
// the versions nor write them back, so a deletion would leave a document's
// versions to the next document stored under its key, and a fold would drop
// them all. An absent file is a store that has not folded since it began to
// keep versions — or one that never did.
func (s *Store[T]) readVersions() error {
	//: a store that keeps none only checks there is nothing to keep.
	if s.keep == 0 {
		_, statErr := fs.Stat(s.fs, s.versionsPath)
		switch {
		//: nothing kept, as configured.
		case errors.Is(statErr, fs.ErrNotExist):
			//: nothing to load.
			return nil
		//: whether there is one is not known.
		case statErr != nil:
			//: LoadFailed, naming the file.
			return loadFailed(s.versionsPath, "unreadable", statErr)
		}
		//: LoadFailed, saying what to do.
		return loadFailed(s.versionsPath, "keeps versions, and the store keeps none: open it with Versions, or remove the file to drop them", nil)
	}
	raw, readErr := fs.ReadFile(s.fs, s.versionsPath)
	//: none written yet.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: every document is as it was before versions were kept.
		return nil
	}
	//: there, and unreadable.
	if readErr != nil {
		//: LoadFailed, naming the file.
		return loadFailed(s.versionsPath, "unreadable", readErr)
	}
	var records map[string]*versionsRecord
	//: not a JSON object from key to versions.
	if decodeErr := json.Unmarshal(raw, &records); decodeErr != nil {
		//: LoadFailed, saying where the JSON broke and never what it held.
		return kerrs.Wrap(coredocstore.LoadFailed, kerrs.WrapParams{},
			kerrs.String("file", s.versionsPath), kerrs.String("problem", "not a versions file"), kerrs.String("cause", jsonCause(decodeErr)))
	}
	//: a JSON null decodes into no map at all.
	if records == nil {
		//: LoadFailed, naming the file.
		return loadFailed(s.versionsPath, "is null, not a versions file", nil)
	}
	//: every document's versions under its key.
	for key, record := range records {
		//: versions nobody could ask for.
		if key == "" {
			//: LoadFailed, naming the file.
			return loadFailed(s.versionsPath, "holds versions under the empty key", nil)
		}
		//: numbered from 1, newest first, each holding a document.
		if recordErr := checkRecord(s.versionsPath, record); recordErr != nil {
			//: LoadFailed, naming the file and never the key.
			return recordErr
		}
		s.versions[key] = compacted(record)
	}
	//: loaded.
	return nil
}

// versionsWithoutDocument refuses versions of a key the store holds no
// document under. No write leaves any — a deletion takes them in its own
// entry, and an entry keeping versions always holds a document — so they come
// from an edited versions file.
func (s *Store[T]) versionsWithoutDocument() error {
	//: every key that has versions.
	for key := range s.versions {
		//: its document.
		if _, found := s.docs[key]; !found {
			//: LoadFailed, naming the file and never the key.
			return loadFailed(s.versionsPath, "holds versions of a document the store does not hold", nil)
		}
	}
	//: every version belongs to a document.
	return nil
}

// checkRecord refuses, as LoadFailed naming file, versions no store wrote:
// a store numbers them from 1, the current one the highest, and keeps the
// former ones newest first, each holding a document.
func checkRecord(file string, record *versionsRecord) error {
	//: a key mapped to null.
	if record == nil {
		//: not versions.
		return loadFailed(file, "holds versions that are null", nil)
	}
	newer := record.Current.Number
	//: numbering starts at 1.
	if newer == 0 {
		//: not a number a store gives.
		return loadFailed(file, "holds a version numbered 0", nil)
	}
	//: newest first, each below the one before.
	for _, former := range record.Former {
		switch {
		//: numbering starts at 1.
		case former.Number == 0:
			//: not a number a store gives.
			return loadFailed(file, "holds a version numbered 0", nil)
		//: a number given twice, or out of order.
		case former.Number >= newer:
			//: not an order a store keeps.
			return loadFailed(file, "holds versions out of order", nil)
		//: a version holds a document.
		case len(former.Document) == 0:
			//: not a version.
			return loadFailed(file, "holds a version without a document", nil)
		//: a version a store wrote.
		default:
		}
		newer = former.Number
	}
	//: versions a store wrote.
	return nil
}

// compacted returns record with its former versions' documents compact, as
// a write makes them: the files hold them indented. A nil record stays nil.
func compacted(record *versionsRecord) *versionsRecord {
	//: nothing recorded.
	if record == nil {
		//: nil.
		return nil
	}
	//: a freshly decoded record is the loader's own to change.
	for i := range record.Former {
		record.Former[i].Document = compactJSON(record.Former[i].Document)
	}
	//: as a write would have left it.
	return record
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
	return kerrs.Wrap(coredocstore.LoadFailed, kerrs.WrapParams{}, fields...)
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
