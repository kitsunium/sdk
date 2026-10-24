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

// supersededWhy is the SNAPSHOT_CORRUPT of a sink another install of the
// same id superseded: the leader answers it with a new offer.
const supersededWhy = "spool superseded by another install of the same id"

// fileSnapshots is the shipped SnapshotStore: ADR 0056's publication rules
// applied to a stream (D4). A snapshot is spooled to <id>.spool, synced,
// renamed to <id>.snap, and published by its <id>.meta, written atomically
// last; a .snap without its .meta is not a snapshot.
type fileSnapshots struct {
	dir  string
	mu   sync.Mutex
	pins map[string]int
	// writing counts the sinks open on each id's spool: a spool a sink
	// still writes to is never swept from under it.
	writing map[string]int
	// owners is the one sink each id's spool belongs to (#547).
	owners map[string]*fileSink
	// resume is, per id, where the spool of a sink released with nothing
	// written resumes: the next Create of that id positions it there
	// without reading it back (#643).
	resume map[string]resumePoint
	// afterPublish, when set by a test, runs in Create once its sink owns
	// the id's spool and before the sink it took the spool from is
	// superseded (#582).
	afterPublish func(id string)
}

// newFileSnapshots opens the snapshot directory dir, creating it private
// to the owner, and removes every .snap that has no .meta: a crash between
// a snapshot's rename and its .meta leaves one, and it is neither a snapshot
// nor resumable. Of the receive spools only the newest stays, the one a
// resume may still continue (#527). A directory that cannot be created, listed or cleaned is a
// STORAGE_FAILED, so a store never opens over a state it could not read.
// syncFailed builds the STORAGE_FAILED of a failed directory sync: a data
// directory's open passes its log's syncFailedAtOpen, which writes the
// markers first (#637), and a store opened alone passes storageFailed.
func newFileSnapshots(dir string, syncFailed func(op string, cause error) error) (*fileSnapshots, error) {
	if err := mkdirDurable(dir); err != nil {
		return nil, err
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
	s := &fileSnapshots{dir: dir, pins: map[string]int{}, writing: map[string]int{}, owners: map[string]*fileSink{}, resume: map[string]resumePoint{}}
	if err := s.sweepSpools(newestSpool(ents)); err != nil {
		return nil, err
	}
	// A process killed after a publication's rename and before its
	// directory sync leaves a snapshot the listing reads and the disk may
	// not hold: the directory is made durable before anything is restored
	// or offered from it, as the log's segments are (syncAtOpen).
	if err := syncDirAtOpen(dir); err != nil {
		return nil, syncFailed("sync snapshots at open", err)
	}
	return s, nil
}

// newestSpool is the id of the newest receive spool among ents, "" when
// there is none. Snapshot ids are fixed-width (index, term), so the newest
// sorts last.
func newestSpool(ents []os.DirEntry) string {
	newest := ""
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".spool"); ok && id > newest {
			newest = id
		}
	}
	return newest
}

// sweepSpools removes every receive spool but keep's and those a sink still
// writes to. A transfer cut mid-stream keeps its spool so that the same
// offer resumes; once another id is accepted, or the directory reopened, no
// offer of the old id is awaited any more, and its spool — up to one whole
// snapshot — stayed on the data disk for good, one more at every such cut
// (#527).
func (s *fileSnapshots) sweepSpools(keep string) error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return storageFailed("list snapshots", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	orphans := s.orphanSpools(ents, keep)
	for _, name := range orphans {
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return storageFailed("remove orphan spool", err)
		}
		delete(s.resume, strings.TrimSuffix(name, ".spool"))
	}
	if len(orphans) == 0 {
		return nil
	}
	if err := syncDir(s.dir); err != nil {
		return storageFailed("remove orphan spool", err)
	}
	return nil
}

// orphanSpools is the names of the spools among ents that are neither
// keep's nor written to by a sink. The caller holds s.mu.
func (s *fileSnapshots) orphanSpools(ents []os.DirEntry, keep string) []string {
	var out []string
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".spool"); ok && id != keep && s.writing[id] == 0 {
			out = append(out, e.Name())
		}
	}
	return out
}

// validID reports whether id may name a snapshot's files: non-empty,
// bounded, and with no separator or dot, so an id a peer sent can never
// reach outside the directory or collide with another file's suffix.
func validID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\.") && filepath.Base(id) == id
}

type fileSink struct {
	s    *fileSnapshots
	id   string
	f    *os.File
	meta coreconsensus.SnapshotMetaValue
	done sync.Once
	// mu orders the sink's writes, Commit, Abort and release against a
	// Create of the same id that supersedes it.
	mu sync.Mutex
	// superseded is set once another sink owns the id's spool: this sink's
	// handle is closed then, and it never touches the spool again (#547).
	superseded bool
	// start is the offset Create positioned the spool at, and wrote is set
	// by the sink's first write: a sink released with nothing written
	// leaves its spool resumable at start (#643).
	start int64
	wrote bool
}

// resumePoint is where a released spool resumes, for an offer of the same
// chunk and encoded sizes: another encoding of the snapshot is read back.
type resumePoint struct {
	off, chunk, size int64
}

// Create implements SnapshotStore: an existing spool of that id resumes from
// its last whole, checksummed chunk (R8-21), found reading the spool one
// chunk at a time — or, when a sink positioned it and was released with
// nothing written, from where that sink stood, with nothing read back: an
// offer whose judgment outlasted the sender's wait for its accept read a
// cold spool back whole at every offer after it, and never resumed (#643).
// Every other spool no sink writes to is removed: accepting this id
// abandons them (#527).
func (s *fileSnapshots) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	if !validID(id) {
		return nil, 0, origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	s.mu.Lock()
	s.writing[id]++
	s.mu.Unlock()
	sink := &fileSink{s: s, id: id, meta: meta}
	if err := s.sweepSpools(id); err != nil {
		sink.closed()
		return nil, 0, err
	}
	// The sink is held until its spool is positioned: a Create of the same
	// id that supersedes it in the meantime waits, then closes the handle
	// this one opened (#582).
	sink.mu.Lock()
	old, at, kept := s.claimSpool(sink)
	if s.afterPublish != nil {
		s.afterPublish(id)
	}
	if old != nil {
		// Locks go from the younger sink to the older one, never back.
		old.supersede()
	}
	f, err := os.OpenFile(filepath.Join(s.dir, id+".spool"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		sink.mu.Unlock()
		sink.closed()
		return nil, 0, storageFailed("spool", err)
	}
	sink.f = f
	off, err := positionSpool(f, old != nil, at, kept, &meta)
	if err != nil {
		err = alsoFailed(storageFailed("spool", err), "close", f.Close())
		sink.f = nil
		sink.mu.Unlock()
		sink.closed()
		return nil, 0, err
	}
	sink.start = off
	sink.mu.Unlock()
	return sink, off, nil
}

// claimSpool claims sink's id's spool for it (claim), and takes the resume
// point a released sink left there, if any.
func (s *fileSnapshots) claimSpool(sink *fileSink) (old *fileSink, at resumePoint, kept bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old = s.claim(sink)
	at, kept = s.resume[sink.id]
	delete(s.resume, sink.id)
	return old, at, kept
}

// positionSpool positions the spool f a new sink owns. Superseding another
// sink, it starts empty: what another install wrote there is not resumed
// from. With a resume point kept for an offer of the same chunk and encoded
// sizes, it resumes there; otherwise at the last whole chunk read back.
func positionSpool(f spoolFile, superseding bool, at resumePoint, kept bool, meta *coreconsensus.SnapshotMetaValue) (int64, error) {
	switch {
	case superseding:
		return 0, f.Truncate(0)
	case kept && at.chunk == meta.ChunkSize && at.size == meta.EncodedSize:
		return resumeAt(f, at.off, meta.ChunkSize)
	default:
		return resumeOffset(f, meta.ChunkSize)
	}
}

// claim makes sink the owner of its id's spool and returns the sink it took
// the spool from, if any. A spool another sink still writes to is never
// shared: Create supersedes that sink, its handle closed, before it opens
// the spool, and writes it from offset 0. Shared, a stale install's Abort
// unlinked the spool the current leader's install of the same id wrote,
// whose Commit then failed STORAGE_FAILED and stopped a node with a
// healthy disk (#547). A sink whose open then fails owns nothing once
// closed. The caller holds s.mu, never the old sink's mu: supersede waits
// for that one outside s.mu (#582).
func (s *fileSnapshots) claim(sink *fileSink) *fileSink {
	old := s.owners[sink.id]
	s.owners[sink.id] = sink
	return old
}

// supersede closes the sink's handle and marks it superseded; it waits for
// a write, Commit, Abort or Create in progress. A sink whose Create failed
// before its open has no handle. The caller holds no lock but its own
// sink's mu.
func (k *fileSink) supersede() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.superseded {
		k.superseded = true
		if k.f != nil {
			// Nothing reads what this sink wrote again.
			abandon(k.f.Close())
		}
	}
}

// spoolFile is what a resume needs of a spool's file.
type spoolFile interface {
	io.ReaderAt
	io.Seeker
	Stat() (os.FileInfo, error)
	Truncate(size int64) error
}

// resumeOffset cuts the spool f back to its last whole chunk, of decoded
// chunks of chunk bytes, and positions it there.
func resumeOffset(f spoolFile, chunk int64) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	off, err := alignedOffsetAt(f, st.Size(), maxEncodedChunk(chunk))
	if err != nil {
		return 0, err
	}
	if err := f.Truncate(off); err != nil {
		return 0, err
	}
	_, err = f.Seek(off, io.SeekStart)
	return off, err
}

// resumeAt cuts the spool f back to off, where a released sink left it, and
// positions it there. An off past the file's end, which only a change made
// outside the store could leave, is found by reading the spool back.
func resumeAt(f spoolFile, off, chunk int64) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if off < 0 || off > st.Size() {
		return resumeOffset(f, chunk)
	}
	if err := f.Truncate(off); err != nil {
		return 0, err
	}
	_, err = f.Seek(off, io.SeekStart)
	return off, err
}

// forget drops the resume point of id, whose spool is published or gone.
func (s *fileSnapshots) forget(id string) {
	s.mu.Lock()
	delete(s.resume, id)
	s.mu.Unlock()
}

// closed counts the sink's spool as no longer written to, and the sink as
// no longer its owner, once.
func (k *fileSink) closed() {
	k.done.Do(func() {
		k.s.mu.Lock()
		if k.s.writing[k.id]--; k.s.writing[k.id] <= 0 {
			delete(k.s.writing, k.id)
		}
		if k.s.owners[k.id] == k {
			delete(k.s.owners, k.id)
		}
		k.s.mu.Unlock()
	})
}

// Write appends p to the spool file; nothing is durable until Commit syncs
// it. A superseded sink writes nothing.
func (k *fileSink) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.superseded {
		return 0, snapshotCorrupt(supersededWhy)
	}
	if len(p) > 0 {
		k.wrote = true
	}
	return k.f.Write(p)
}

// Commit publishes the spool in ADR 0056's order: the encoded size is
// checked, the spool synced and closed, renamed to <id>.snap, and the
// <id>.meta written atomically LAST, so a crash at any step leaves no
// published snapshot without its bytes. A superseded sink, or a spool gone
// from under its sink, is a SNAPSHOT_CORRUPT the leader answers with a new
// offer, never this node's disk failing (#547). A Create of the same id
// waits for a Commit in progress.
func (k *fileSink) Commit(final coreconsensus.SnapshotMetaValue) error {
	defer k.closed()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.superseded {
		return snapshotCorrupt(supersededWhy)
	}
	final.ID = k.id
	size, err := k.f.Seek(0, io.SeekCurrent)
	if err != nil {
		// The size could not be read: the disk failed, the spool did not
		// lie about its length.
		return alsoFailed(storageFailed("snapshot size", err), "close", k.f.Close())
	}
	if size != final.EncodedSize {
		return alsoFailed(snapshotCorrupt("encoded size"), "close", k.f.Close())
	}
	if err := k.f.Sync(); err != nil {
		return alsoFailed(storageFailed("sync snapshot", err), "close", k.f.Close())
	}
	if err := k.f.Close(); err != nil {
		return storageFailed("close snapshot", err)
	}
	k.s.forget(k.id)
	if err := os.Rename(filepath.Join(k.s.dir, k.id+".spool"), filepath.Join(k.s.dir, k.id+".snap")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshotCorrupt("spool gone before its commit")
		}
		return storageFailed("publish snapshot", err)
	}
	if err := writeFileSync(filepath.Join(k.s.dir, k.id+".meta"), wire.AppendSnapshotMeta(nil, &final)); err != nil {
		return storageFailed("publish snapshot meta", err)
	}
	return nil
}

// published reads the snapshot a directory entry named name publishes. ok
// is false for anything that is not a complete snapshot: another file, a
// .meta that does not parse, names another id or a position its id does
// not name (#546), or one whose .snap is missing. Such an entry is
// skipped: it is a publication a crash interrupted or a .meta that rotted,
// not a snapshot to restore from. A .meta or a .snap that is there
// but cannot be read is neither: it is an I/O failure, and it is reported,
// so that a listing never reads a failing disk as "no snapshot".
func (s *fileSnapshots) published(name string) (coreconsensus.SnapshotMetaValue, bool, error) {
	var m coreconsensus.SnapshotMetaValue
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
	if wire.ParseSnapshotMeta(b, &m) != nil || m.ID != id || !idNamesPosition(&m) {
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
// checksums hold, so nothing it could say changes what is kept. A sink that
// wrote nothing leaves the spool where Create positioned it, and the next
// Create resumes there without reading it back (#643).
func (k *fileSink) release() {
	defer k.closed()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.superseded {
		return
	}
	abandon(k.f.Close())
	if !k.wrote {
		k.s.mu.Lock()
		k.s.resume[k.id] = resumePoint{off: k.start, chunk: k.meta.ChunkSize, size: k.meta.EncodedSize}
		k.s.mu.Unlock()
	}
}

// Abort closes and deletes the spool; a spool already gone is not a
// failure, any other removal failure is a STORAGE_FAILED. A spool another
// sink superseded is that sink's and stays (#547).
func (k *fileSink) Abort() error {
	defer k.closed()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.superseded {
		return nil
	}
	// The spool is being discarded: what its descriptor's Close could say
	// about bytes no one will read changes nothing. The removal is what
	// Abort promises, and its failure is reported.
	abandon(k.f.Close())
	k.s.forget(k.id)
	if err := os.Remove(filepath.Join(k.s.dir, k.id+".spool")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storageFailed("abort spool", err)
	}
	return nil
}

// List implements SnapshotStore: complete snapshots, newest first.
func (s *fileSnapshots) List() ([]coreconsensus.SnapshotMetaValue, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, storageFailed("list snapshots", err)
	}
	var out []coreconsensus.SnapshotMetaValue
	for _, e := range ents {
		m, ok, err := s.published(e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b coreconsensus.SnapshotMetaValue) int {
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
	s    *fileSnapshots
	meta coreconsensus.SnapshotMetaValue
	once sync.Once
}

// Meta returns the metadata read from the snapshot's .meta at Open.
func (h *fileHandle) Meta() coreconsensus.SnapshotMetaValue { return h.meta }

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
func (s *fileSnapshots) Open(id string) (coreconsensus.SnapshotHandle, error) {
	if !validID(id) {
		return nil, origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id+".meta"))
	if err != nil {
		return nil, origin(coreconsensus.SnapshotCorrupt, errs.String("id", id), errs.String("why", "no meta"))
	}
	var m coreconsensus.SnapshotMetaValue
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
func (s *fileSnapshots) Remove(id string) error {
	if !validID(id) {
		return origin(coreconsensus.FrameInvalid, errs.String("snapshot_id", id))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pins[id] > 0 {
		return pinnedSnapshot()
	}
	for _, ext := range []string{".meta", ".snap"} {
		if err := os.Remove(filepath.Join(s.dir, id+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return storageFailed("remove", err)
		}
	}
	if err := syncDir(s.dir); err != nil {
		return storageFailed("remove", err)
	}
	return nil
}
