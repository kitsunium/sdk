package consensus

import (
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// The fault-injecting filesystem shim of ADR 0152 D15: a faultFS stands
// behind the one fault seam of a data directory (diskFS), counts every
// operation that changes the disk — an open to write, a write, a
// truncation, a sync or fdatasync, a directory sync, a rename, a removal —
// and either fails operation at (EIO, ENOSPC for a write) and lets the rest
// through, or stops the process model there: that operation and every one
// after it fail, and settle then applies the simulator's disk model to the
// real files, the bytes written since a file's last sync kept, dropped, or
// torn at a 4 KiB boundary by a seed. A directory entry — a rename, a
// removal, a creation — is taken as durable once its call returns; what a
// directory sync guards is the order the code keeps, which the operations'
// trace shows, not a state this model reverts. Tests that need one moment
// of a write read it off the operations (before, after), never off a hook
// of the log's own.

// faultKind is what a faultFS does at its operation at.
type faultKind uint8

const (
	// faultNone lets every operation through.
	faultNone faultKind = iota
	// faultFail fails operation at, EIO or ENOSPC, and lets the rest
	// through: a device that refused one request.
	faultFail
	// faultCrash stops the process model at operation at: it and every
	// later one fail, and nothing more reaches the disk.
	faultCrash
)

// cutKind is what a power cut does to the bytes written since a file's
// last sync (settle).
type cutKind uint8

const (
	// cutKeep keeps them: the page cache was written back.
	cutKeep cutKind = iota
	// cutDrop drops them: the file is what its last sync left.
	cutDrop
	// cutTear keeps some 4 KiB pages of them and drops the others, by the
	// seed.
	cutTear
)

// String names the cut in a subtest's name.
func (c cutKind) String() string {
	return [...]string{"keep", "drop", "tear"}[c]
}

// pageWrite is one change to a file since its last sync: the bytes written
// at off, the bytes they replaced (short at the file's end) and the file's
// size before it. A truncation is one too, its data empty.
type pageWrite struct {
	off  int64
	data []byte
	old  []byte
	size int64
}

// diskOp is one operation the shim saw. n is its index, from 1; kind is
// open, write, truncate, sync, datasync, syncdir, rename, remove or
// removeall; to is a rename's target; dirty is, for a sync, the writes it
// makes durable, in order.
type diskOp struct {
	n     int
	kind  string
	path  string
	to    string
	off   int64
	data  []byte
	file  segFile
	dirty []pageWrite
}

// errStopped is what every operation answers once the process model
// stopped (faultCrash).
var errStopped = errors.New("the process model stopped at an earlier operation")

// faultFS is the shim. The zero value is not usable: newFaultFS.
type faultFS struct {
	mu      sync.Mutex
	ops     int
	at      int
	kind    faultKind
	stopped bool
	// realSync passes the syncs to the disk; off, a sync only marks the
	// bytes durable in the model, which the shim lane needs alone.
	realSync bool
	// inline runs the background zero-fill at once, so that an index names
	// the same operation at every run.
	inline bool
	dirty  map[string][]pageWrite
	trace  []string
	// before runs before an operation reaches the disk, after the fault
	// decision; an error fails the operation with it. after runs once it
	// did. Neither runs under the shim's lock.
	before func(op *diskOp) error
	after  func(op *diskOp)
}

// newFaultFS is a shim that lets every operation through.
func newFaultFS() *faultFS { return &faultFS{dirty: map[string][]pageWrite{}} }

// disk is the seam over the shim.
func (s *faultFS) disk() *diskFS {
	return &diskFS{
		openFile:  s.openFile,
		datasync:  func(f segFile) error { return s.sync(f, "datasync") },
		syncDir:   s.syncDir,
		rename:    s.rename,
		remove:    s.remove,
		removeAll: s.removeAll,
		background: func(run func()) {
			if s.inline {
				run()
				return
			}
			go run()
		},
	}
}

// count is the number of operations the shim has seen.
func (s *faultFS) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ops
}

// opAt is operation k of the trace, as "k kind name".
func (s *faultFS) opAt(k int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k < 1 || k > len(s.trace) {
		return "none"
	}
	return s.trace[k-1]
}

// begin counts op and decides its fate: the injected failure, the stop,
// a test's before, or nil, which lets it reach the disk.
func (s *faultFS) begin(op *diskOp) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return &fs.PathError{Op: op.kind, Path: op.path, Err: errStopped}
	}
	s.ops++
	op.n = s.ops
	s.trace = append(s.trace, strconv.Itoa(op.n)+" "+op.kind+" "+filepath.Base(op.path))
	hit := s.kind != faultNone && op.n == s.at
	if hit && s.kind == faultCrash {
		s.stopped = true
	}
	if op.kind == "sync" || op.kind == "datasync" {
		op.dirty = slices.Clone(s.dirty[op.path])
	}
	s.mu.Unlock()
	switch {
	case hit && s.kind == faultCrash:
		return &fs.PathError{Op: op.kind, Path: op.path, Err: errStopped}
	case hit && op.kind == "write":
		return &fs.PathError{Op: op.kind, Path: op.path, Err: syscall.ENOSPC}
	case hit:
		return &fs.PathError{Op: op.kind, Path: op.path, Err: syscall.EIO}
	case s.before != nil:
		return s.before(op)
	}
	return nil
}

// end runs a test's after once op reached the disk.
func (s *faultFS) end(op *diskOp) {
	if s.after != nil {
		s.after(op)
	}
}

// note records a change of path about to be made: off, the bytes data, or
// a truncation to size when data is nil and truncate is set.
func (s *faultFS) note(path string, off int64, data []byte, truncate bool, size int64) {
	cur := fileSize(path)
	w := pageWrite{off: off, data: slices.Clone(data), size: cur}
	end := off + int64(len(data))
	if truncate {
		off, end = size, cur
		w.off = size
	}
	if end > off && off < cur {
		w.old = readRange(path, off, min(end, cur)-off)
	}
	s.mu.Lock()
	s.dirty[path] = append(s.dirty[path], w)
	s.mu.Unlock()
}

// fileSize is path's size, 0 when it cannot be read.
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// readRange reads n bytes of path at off through a descriptor of its own:
// the written one may be write-only.
func readRange(path string, off, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b := make([]byte, n)
	m, _ := f.ReadAt(b, off)
	return b[:m]
}

// openFile opens name through the shim; a truncating open of a file that
// holds bytes is a change of the file.
func (s *faultFS) openFile(name string, flag int, perm os.FileMode) (segFile, error) {
	op := &diskOp{kind: "open", path: name}
	if err := s.begin(op); err != nil {
		return nil, err
	}
	if flag&os.O_TRUNC != 0 && fileSize(name) > 0 {
		s.note(name, 0, nil, true, 0)
	}
	f, err := openOSFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	w := s.wrap(f, name, flag&os.O_APPEND != 0)
	op.file = w
	s.end(op)
	return w, nil
}

// wrap puts f, open at path, behind the shim.
func (s *faultFS) wrap(f segFile, path string, appending bool) segFile {
	return &faultFile{segFile: f, s: s, path: path, appending: appending}
}

// sync makes f's bytes durable in the model, and on the disk when realSync
// is set; kind is sync or datasync.
func (s *faultFS) sync(f segFile, kind string) error {
	inner, path := f, f.Name()
	if w, ok := f.(*faultFile); ok {
		inner, path = w.segFile, w.path
	}
	op := &diskOp{kind: kind, path: path, file: f}
	if err := s.begin(op); err != nil {
		return err
	}
	if s.realSync {
		var err error
		if kind == "datasync" {
			err = datasyncFile(inner)
		} else {
			err = inner.Sync()
		}
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	if rest := s.dirty[path]; len(rest) >= len(op.dirty) {
		s.dirty[path] = rest[len(op.dirty):]
	}
	if len(s.dirty[path]) == 0 {
		delete(s.dirty, path)
	}
	s.mu.Unlock()
	s.end(op)
	return nil
}

// syncDir syncs a directory through the shim.
func (s *faultFS) syncDir(dir string) error {
	op := &diskOp{kind: "syncdir", path: dir}
	if err := s.begin(op); err != nil {
		return err
	}
	if s.realSync {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	s.end(op)
	return nil
}

// rename renames through the shim; what was written to from and not synced
// follows it.
func (s *faultFS) rename(from, to string) error {
	op := &diskOp{kind: "rename", path: from, to: to}
	if err := s.begin(op); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.dirty, to)
	if w, ok := s.dirty[from]; ok {
		s.dirty[to] = w
		delete(s.dirty, from)
	}
	s.mu.Unlock()
	s.end(op)
	return nil
}

// remove removes through the shim.
func (s *faultFS) remove(name string) error {
	op := &diskOp{kind: "remove", path: name}
	if err := s.begin(op); err != nil {
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.dirty, name)
	s.mu.Unlock()
	s.end(op)
	return nil
}

// removeAll removes a tree through the shim.
func (s *faultFS) removeAll(path string) error {
	op := &diskOp{kind: "removeall", path: path}
	if err := s.begin(op); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	s.mu.Lock()
	for p := range s.dirty {
		if p == path || strings.HasPrefix(p, path+string(filepath.Separator)) {
			delete(s.dirty, p)
		}
	}
	s.mu.Unlock()
	s.end(op)
	return nil
}

// settle is the power cut: the bytes written since each file's last sync
// are kept, dropped, or torn at 4 KiB pages by seed, on the real files,
// past the shim. The model starts again from a clean disk.
func (s *faultFS) settle(cut cutKind, seed uint64) error {
	s.mu.Lock()
	dirty := s.dirty
	s.dirty = map[string][]pageWrite{}
	s.mu.Unlock()
	if cut == cutKeep {
		return nil
	}
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	paths := make([]string, 0, len(dirty))
	for p := range dirty {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		if err := revert(p, dirty[p], cut, rng); err != nil {
			return err
		}
	}
	return nil
}

// revert undoes ws on path, newest first: all of them for cutDrop, the
// file's size included, and a random half of their 4 KiB pages for cutTear,
// the size kept, a page past the old end read back as zeros.
func revert(path string, ws []pageWrite, cut cutKind, rng *rand.Rand) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	for i := len(ws) - 1; i >= 0; i-- {
		w := ws[i]
		if cut == cutDrop {
			if err := restore(f, w, w.off, w.off+max(int64(len(w.data)), int64(len(w.old)))); err != nil {
				return err
			}
			if err := f.Truncate(w.size); err != nil {
				return err
			}
			continue
		}
		end := w.off + int64(len(w.data))
		for p := w.off &^ 4095; p < end; p += 4096 {
			if rng.IntN(2) == 0 {
				continue
			}
			if err := restore(f, w, max(p, w.off), min(p+4096, end)); err != nil {
				return err
			}
		}
	}
	return nil
}

// restore writes back the bytes w replaced over [lo, hi), zeros past what
// it replaced.
func restore(f *os.File, w pageWrite, lo, hi int64) error {
	if hi <= lo {
		return nil
	}
	b := make([]byte, hi-lo)
	if o := lo - w.off; o < int64(len(w.old)) {
		copy(b, w.old[o:])
	}
	_, err := f.WriteAt(b, lo)
	return err
}

// faultFile is a file behind the shim: its writes, truncations and syncs
// are operations; its reads and its Close are not.
type faultFile struct {
	segFile
	s         *faultFS
	path      string
	appending bool
}

// WriteAt writes through the shim.
func (f *faultFile) WriteAt(b []byte, off int64) (int, error) {
	op := &diskOp{kind: "write", path: f.path, off: off, data: b, file: f}
	if err := f.s.begin(op); err != nil {
		return 0, err
	}
	f.s.note(f.path, off, b, false, 0)
	n, err := f.segFile.WriteAt(b, off)
	if err == nil {
		f.s.end(op)
	}
	return n, err
}

// Write writes at the file's offset, its end when it appends, through the
// shim.
func (f *faultFile) Write(b []byte) (int, error) {
	off := fileSize(f.path)
	if !f.appending {
		cur, err := f.segFile.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}
		off = cur
	}
	op := &diskOp{kind: "write", path: f.path, off: off, data: b, file: f}
	if err := f.s.begin(op); err != nil {
		return 0, err
	}
	f.s.note(f.path, off, b, false, 0)
	n, err := f.segFile.Write(b)
	if err == nil {
		f.s.end(op)
	}
	return n, err
}

// Truncate truncates through the shim.
func (f *faultFile) Truncate(size int64) error {
	op := &diskOp{kind: "truncate", path: f.path, off: size, file: f}
	if err := f.s.begin(op); err != nil {
		return err
	}
	f.s.note(f.path, 0, nil, true, size)
	if err := f.segFile.Truncate(size); err != nil {
		return err
	}
	f.s.end(op)
	return nil
}

// Sync syncs through the shim.
func (f *faultFile) Sync() error { return f.s.sync(f, "sync") }

// useShim puts the open data directory d behind s: the directory, its log
// — the segments it holds open included — and its snapshot store. It runs
// before the test starts anything on d.
func useShim(d DataDir, s *faultFS) {
	disk := s.disk()
	l := d.d.log
	l.wmu.Lock()
	l.mu.Lock()
	l.disk = disk
	for _, seg := range l.segs {
		seg.f = s.wrap(seg.f, seg.f.Name(), false)
	}
	l.mu.Unlock()
	l.wmu.Unlock()
	d.d.disk = disk
	if d.d.snaps != nil {
		d.d.snaps.disk = disk
	}
}

// isBatchSync reports whether op is the fdatasync of a log batch's write:
// a published segment's, after a write that is no seal (the rotation's own
// sync) — the moment between a batch's write and its sync.
func isBatchSync(op *diskOp) bool {
	if op.kind != "datasync" || len(op.dirty) == 0 || !strings.HasPrefix(filepath.Base(op.path), "seg-") || strings.HasSuffix(op.path, ".tmp") {
		return false
	}
	r, ok := parseRecord(op.dirty[0].data)
	return !ok || r.kind != recSeal
}

// onBatchSync runs fn between each write of a batch of d's log and its
// fdatasync — twice for a batch of several records, the body's then the
// end record's — on the writer's goroutine, the log's lock released. The
// syncs still reach the disk.
func onBatchSync(d DataDir, fn func()) *faultFS {
	s := newFaultFS()
	s.realSync = true
	s.before = func(op *diskOp) error {
		if isBatchSync(op) {
			fn()
		}
		return nil
	}
	useShim(d, s)
	return s
}

// The shim's own model: a write not synced is what a cut drops or tears, a
// synced one stays, a rename carries what was not synced with it, and an
// operation is failed or stopped at the index it is told.
func TestTheFaultShimModelsWhatASyncMakesDurable(t *testing.T) {
	dir := t.TempDir()
	s := newFaultFS()
	disk := s.disk()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	f, err := disk.openFile(a, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("synced"), 0); err != nil {
		t.Fatal(err)
	}
	if err := disk.datasync(f); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, 8192), 6); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := disk.rename(a, b); err != nil {
		t.Fatal(err)
	}
	if got := s.count(); got != 5 {
		t.Fatalf("%d operations counted, want 5: %v", got, s.trace)
	}
	if err := s.settle(cutDrop, 1); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(b); err != nil || string(got) != "synced" {
		t.Fatalf("after a cut that drops what was not synced: %q %v", got, err)
	}
	s.kind, s.at = faultFail, s.count()+1
	if err := disk.syncDir(dir); !errors.Is(err, syscall.EIO) {
		t.Fatalf("operation %d told to fail: %v", s.at, err)
	}
	s.kind, s.at = faultCrash, s.count()+1
	if err := disk.remove(b); !errors.Is(err, errStopped) {
		t.Fatalf("operation %d told to stop the process: %v", s.at, err)
	}
	if err := disk.syncDir(dir); !errors.Is(err, errStopped) {
		t.Fatalf("an operation past the stop: %v", err)
	}
	if _, err := os.Stat(b); err != nil {
		t.Fatalf("the stopped removal reached the disk: %v", err)
	}
}
