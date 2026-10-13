package consensus

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// FileSnapshots is the shipped SnapshotStore: ADR 0056's publication rules
// applied to a stream (D4). A snapshot is spooled to <id>.spool, synced,
// renamed to <id>.snap, and published by its <id>.meta, written atomically
// last; a .snap without its .meta is not a snapshot.
type FileSnapshots struct {
	dir  string
	mu   sync.Mutex
	pins map[string]int
}

// newFileSnapshots opens the snapshot directory dir, creating it private
// to the owner, and removes every .snap that has no .meta: a crash between
// a snapshot's rename and its .meta leaves one, and it is neither a snapshot
// nor resumable. A directory that cannot be created, listed or cleaned is a
// STORAGE_FAILED, so a store never opens over a state it could not read.
func newFileSnapshots(dir string) (*FileSnapshots, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, storageFailed("mkdir", err)
	}
	// A crash between a snapshot's rename and its .meta leaves a .snap
	// that is not a snapshot, nor resumable: it goes (review of PR #301, L4).
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, storageFailed("list snapshots", err)
	}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".snap")
		if !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, id+".meta")); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return nil, storageFailed("remove orphan snapshot", err)
			}
		}
	}
	return &FileSnapshots{dir: dir, pins: map[string]int{}}, nil
}

// validID reports whether id may name a snapshot's files: non-empty,
// bounded, and with no separator or dot, so an id a peer sent can never
// reach outside the directory or collide with another file's suffix.
func validID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\.") && filepath.Base(id) == id
}

type fileSink struct {
	s    *FileSnapshots
	id   string
	f    *os.File
	meta coreconsensus.SnapshotMeta
}

// Create implements SnapshotStore: an existing spool of that id resumes from
// its last whole, checksummed chunk (R8-21).
func (s *FileSnapshots) Create(id string, meta coreconsensus.SnapshotMeta) (coreconsensus.SnapshotSink, int64, error) {
	if !validID(id) {
		return nil, 0, origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	path := filepath.Join(s.dir, id+".spool")
	sink := &fileSink{s: s, id: id, meta: meta}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, 0, storageFailed("spool", err)
	}
	sink.f = f
	prefix, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, alsoFailed(storageFailed("spool", err), "close", f.Close())
	}
	off := alignedOffset(prefix)
	if err := f.Truncate(off); err != nil {
		return nil, 0, alsoFailed(storageFailed("spool", err), "close", f.Close())
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, 0, alsoFailed(storageFailed("spool", err), "close", f.Close())
	}
	return sink, off, nil
}

// Write appends p to the spool file; nothing is durable until Commit syncs
// it.
func (k *fileSink) Write(p []byte) (int, error) { return k.f.Write(p) }

// Commit publishes the spool in ADR 0056's order: the encoded size is
// checked, the spool synced and closed, renamed to <id>.snap, and the
// <id>.meta written atomically LAST, so a crash at any step leaves no
// published snapshot without its bytes.
func (k *fileSink) Commit(final coreconsensus.SnapshotMeta) error {
	final.ID = k.id
	size, err := k.f.Seek(0, io.SeekCurrent)
	if err != nil || size != final.EncodedSize {
		return alsoFailed(snapshotCorrupt("encoded size"), "close", k.f.Close())
	}
	if err := k.f.Sync(); err != nil {
		return alsoFailed(storageFailed("sync snapshot", err), "close", k.f.Close())
	}
	if err := k.f.Close(); err != nil {
		return storageFailed("close snapshot", err)
	}
	snap := filepath.Join(k.s.dir, k.id+".snap")
	if err := os.Rename(filepath.Join(k.s.dir, k.id+".spool"), snap); err != nil {
		return storageFailed("publish snapshot", err)
	}
	if err := writeFileSync(filepath.Join(k.s.dir, k.id+".meta"), wire.AppendSnapshotMeta(nil, &final)); err != nil {
		return storageFailed("publish snapshot meta", err)
	}
	return nil
}

// published reads the snapshot a directory entry named name publishes. ok
// is false for anything that is not a complete snapshot: another file, a
// .meta that does not parse or names another id, or one whose .snap is
// missing. Such an entry is skipped: it is a publication a crash
// interrupted, not a snapshot that failed. A .meta or a .snap that is there
// but cannot be read is neither: it is an I/O failure, and it is reported,
// so that a listing never reads a failing disk as "no snapshot".
func (s *FileSnapshots) published(name string) (coreconsensus.SnapshotMeta, bool, error) {
	var m coreconsensus.SnapshotMeta
	if !strings.HasSuffix(name, ".meta") {
		return m, false, nil
	}
	id := strings.TrimSuffix(name, ".meta")
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, storageFailed("read snapshot meta", err)
	}
	if wire.ParseSnapshotMeta(b, &m) != nil || m.ID != id {
		return m, false, nil
	}
	if _, err := os.Stat(filepath.Join(s.dir, id+".snap")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, false, nil
		}
		return m, false, storageFailed("stat snapshot", err)
	}
	return m, true, nil
}

// release closes the spool's handle and keeps the spool for a resume. A
// failed Close is abandoned: the resume reads back only the chunks whose
// checksums hold, so nothing it could say changes what is kept.
func (k *fileSink) release() { abandon(k.f.Close()) }

// Abort closes and deletes the spool; a spool already gone is not a
// failure, any other removal failure is a STORAGE_FAILED.
func (k *fileSink) Abort() error {
	// The spool is being discarded: what its descriptor's Close could say
	// about bytes no one will read changes nothing. The removal is what
	// Abort promises, and its failure is reported.
	abandon(k.f.Close())
	if err := os.Remove(filepath.Join(k.s.dir, k.id+".spool")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storageFailed("abort spool", err)
	}
	return nil
}

// List implements SnapshotStore: complete snapshots, newest first.
func (s *FileSnapshots) List() ([]coreconsensus.SnapshotMeta, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, storageFailed("list snapshots", err)
	}
	var out []coreconsensus.SnapshotMeta
	for _, e := range ents {
		m, ok, err := s.published(e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b coreconsensus.SnapshotMeta) int {
		switch {
		case a.Position.Index > b.Position.Index:
			return -1
		case a.Position.Index < b.Position.Index:
			return 1
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

type fileHandle struct {
	*os.File
	s    *FileSnapshots
	meta coreconsensus.SnapshotMeta
	once sync.Once
}

// Meta returns the metadata read from the snapshot's .meta at Open.
func (h *fileHandle) Meta() coreconsensus.SnapshotMeta { return h.meta }

// Close unpins the snapshot and closes its file exactly once; a second
// Close returns nil and releases nothing.
func (h *fileHandle) Close() error {
	var err error
	h.once.Do(func() {
		h.s.mu.Lock()
		h.s.pins[h.meta.ID]--
		h.s.mu.Unlock()
		if cerr := h.File.Close(); cerr != nil {
			err = storageFailed("close snapshot", cerr)
		}
	})
	return err
}

// Open implements SnapshotStore: pinned until Close.
func (s *FileSnapshots) Open(id string) (coreconsensus.SnapshotHandle, error) {
	if !validID(id) {
		return nil, origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id+".meta"))
	if err != nil {
		return nil, origin(coreconsensus.SnapshotCorrupt, errs.String("id", id), errs.String("why", "no meta"))
	}
	var m coreconsensus.SnapshotMeta
	if err := wire.ParseSnapshotMeta(b, &m); err != nil {
		return nil, err
	}
	h := &fileHandle{s: s, meta: m}
	f, err := os.Open(filepath.Join(s.dir, id+".snap"))
	if err != nil {
		return nil, storageFailed("open snapshot", err)
	}
	h.File = f
	s.mu.Lock()
	s.pins[id]++
	s.mu.Unlock()
	return h, nil
}

// Remove implements SnapshotStore: refused while a handle is open. The meta
// goes first, so a crash leaves no published snapshot without its bytes.
func (s *FileSnapshots) Remove(id string) error {
	if !validID(id) {
		return origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins[id] > 0 {
		return storageFailed("remove", errPinned)
	}
	for _, ext := range []string{".meta", ".snap"} {
		if err := os.Remove(filepath.Join(s.dir, id+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return storageFailed("remove", err)
		}
	}
	return syncDir(s.dir)
}
