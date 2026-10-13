package consensus

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

const (
	fileLock         = "LOCK"
	fileStamp        = "STAMP"
	markerQuarantine = "QUARANTINE"
	markerStopPrefix = "STOP-"
	fileAudit        = "AUDIT"
)

// DataDirOptions tunes OpenDataDirWith.
type DataDirOptions struct {
	// SegmentBytes is the fixed size of a log segment; 0 is 64 MiB.
	SegmentBytes int64
}

type dataDir struct {
	dir    string
	opts   DataDirOptions
	mu     sync.Mutex
	lockf  *os.File
	log    *FileLog
	snaps  *FileSnapshots
	closed bool
	inUse  bool
}

// DataDir is the shipped layout: a lock, a stamp, the segmented log, the
// snapshots and the markers. A DataDir value wraps a pointer, so copies
// share one lock and one Close (R4-29).
type DataDir struct{ d *dataDir }

// OpenDataDir takes the directory lock FIRST, then reads, scans and repairs
// (D11). A second process fails at once with DATA_DIR_LOCKED.
func OpenDataDir(dir string) (DataDir, error) { return OpenDataDirWith(dir, DataDirOptions{}) }

// OpenDataDirWith is OpenDataDir with options.
func OpenDataDirWith(dir string, opts DataDirOptions) (DataDir, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DataDir{}, storageFailed("mkdir", err)
	}
	d := &dataDir{dir: dir, opts: opts}
	lf, err := os.OpenFile(filepath.Join(dir, fileLock), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return DataDir{}, storageFailed("lock", err)
	}
	d.lockf = lf
	held, err := flock.TryLock(lf)
	if err != nil {
		// The lock could not be asked for: a storage failure, never read as
		// another process holding the directory.
		lf.Close()
		return DataDir{}, storageFailed("lock", err)
	}
	if !held {
		lf.Close()
		return DataDir{}, origin(coreconsensus.DataDirLocked, errs.String("dir", dir))
	}
	if err := d.open(); err != nil {
		// Closing the descriptor releases the lock too; the explicit unlock
		// is what makes the release immediate on Windows, and its failure
		// travels with the cause rather than in its place.
		err = alsoFailed(err, "unlock", flock.Unlock(lf))
		lf.Close()
		return DataDir{}, err
	}
	return DataDir{d: d}, nil
}

func (d *dataDir) open() error {
	l, err := openFileLog(d, d.opts.SegmentBytes)
	if err != nil {
		return err
	}
	s, err := newFileSnapshots(filepath.Join(d.dir, "snap"))
	if err != nil {
		l.Close()
		return err
	}
	d.log, d.snaps = l, s
	return nil
}

// Dir returns the directory.
func (d DataDir) Dir() string { return d.d.dir }

// Log returns the directory's LogStore.
func (d DataDir) Log() coreconsensus.LogStore { return d.d.log }

// Snapshots returns the directory's SnapshotStore.
func (d DataDir) Snapshots() coreconsensus.SnapshotStore { return d.d.snaps }

// Close releases the lock; idempotent, and refused while a Node built on it
// is open (R3-18).
func (d DataDir) Close() error {
	if d.d == nil {
		return nil
	}
	d.d.mu.Lock()
	defer d.d.mu.Unlock()
	if d.d.closed {
		return nil
	}
	if d.d.inUse {
		return origin(coreconsensus.DataDirInUse, errs.String("dir", d.d.dir))
	}
	d.d.closed = true
	// Every step runs whatever the one before it answered, so a failed log
	// close still releases the lock; the first failure is the one reported.
	err := d.d.log.Close()
	if uerr := flock.Unlock(d.d.lockf); uerr != nil && err == nil {
		err = storageFailed("unlock", uerr)
	}
	if cerr := d.d.lockf.Close(); cerr != nil && err == nil {
		err = storageFailed("close lock", cerr)
	}
	return err
}

// offline guards an offline operation: refused while a live Node holds the
// directory (D11).
func (d DataDir) offline() error {
	if d.d == nil {
		return misconfigured("DataDir", "zero")
	}
	d.d.mu.Lock()
	defer d.d.mu.Unlock()
	switch {
	case d.d.closed:
		return origin(coreconsensus.NodeStopped, errs.String("why", "data directory closed"))
	case d.d.inUse:
		return origin(coreconsensus.DataDirInUse, errs.String("dir", d.d.dir))
	}
	return nil
}

func (l *FileLog) acquire() error {
	if l.dd == nil {
		return nil
	}
	l.dd.mu.Lock()
	defer l.dd.mu.Unlock()
	if l.dd.inUse || l.dd.closed {
		return origin(coreconsensus.DataDirInUse, errs.String("dir", l.dd.dir))
	}
	l.dd.inUse = true
	return nil
}

func (l *FileLog) release() {
	if l.dd == nil {
		return
	}
	l.dd.mu.Lock()
	l.dd.inUse = false
	l.dd.mu.Unlock()
}

// ---- stamp ----

const stampVersion = 1

func encodeStamp(s coreconsensus.Stamp) []byte {
	b := []byte{stampVersion}
	b = append(b, s.Log.Cluster[:]...)
	b = binary.LittleEndian.AppendUint64(b, s.Log.Epoch)
	b = append(b, s.Log.Predecessor[:]...)
	b = binary.LittleEndian.AppendUint64(b, s.Log.PredecessorEpoch)
	b = binary.LittleEndian.AppendUint64(b, s.Log.KeptIndex)
	b = binary.LittleEndian.AppendUint64(b, uint64(s.ID))
	b = append(b, s.Incarnation[:]...)
	b = append(b, byte(s.State))
	b = append(b, s.Founding[:]...)
	b = append(b, byte(len(s.Slot)))
	b = append(b, s.Slot...)
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

func decodeStamp(b []byte) (coreconsensus.Stamp, bool) {
	var s coreconsensus.Stamp
	const fixed = 1 + 16 + 8 + 16 + 8 + 8 + 8 + 16 + 1 + 32 + 1
	if len(b) < fixed+4 || b[0] != stampVersion {
		return s, false
	}
	body := b[:len(b)-4]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
		return s, false
	}
	p := 1
	copy(s.Log.Cluster[:], body[p:])
	p += 16
	s.Log.Epoch = binary.LittleEndian.Uint64(body[p:])
	p += 8
	copy(s.Log.Predecessor[:], body[p:])
	p += 16
	s.Log.PredecessorEpoch = binary.LittleEndian.Uint64(body[p:])
	p += 8
	s.Log.KeptIndex = binary.LittleEndian.Uint64(body[p:])
	p += 8
	s.ID = coreconsensus.NodeID(binary.LittleEndian.Uint64(body[p:]))
	p += 8
	copy(s.Incarnation[:], body[p:])
	p += 16
	s.State = coreconsensus.StampState(body[p])
	p++
	copy(s.Founding[:], body[p:])
	p += 32
	n := int(body[p])
	p++
	if p+n != len(body) {
		return s, false
	}
	s.Slot = string(body[p:])
	return s, true
}

func readStamp(dir string) (coreconsensus.Stamp, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileStamp))
	if errors.Is(err, os.ErrNotExist) {
		return coreconsensus.Stamp{}, nil
	}
	if err != nil {
		return coreconsensus.Stamp{}, storageFailed("stamp", err)
	}
	s, ok := decodeStamp(b)
	if !ok {
		return coreconsensus.Stamp{}, origin(coreconsensus.DataDirMismatch, errs.String("why", "unreadable stamp"))
	}
	return s, nil
}

func writeStamp(dir string, s coreconsensus.Stamp) error {
	return writeFileSync(filepath.Join(dir, fileStamp), encodeStamp(s))
}

// ---- stop markers (D4, R7-14, R8-05, R9-05) ----

func (l *FileLog) runtimeMarker(reason string) string {
	sum := sha256.Sum256([]byte(l.dir))
	return filepath.Join(l.runDir, hex.EncodeToString(sum[:8])+"-"+reason)
}

// markStop writes the stop's marker: STORAGE_FAILED in TWO places, the data
// directory and a boot-scoped path, each holding the boot id; the final
// stops in the data directory only. It reports whether a marker was written.
func (l *FileLog) markStop(code errs.Code, detail string) bool {
	reason := reasonOf(code)
	body := []byte(fmt.Sprintf("boot=%s\n%s\n", bootID(), detail))
	written := writeFileSync(filepath.Join(l.dir, markerStopPrefix+reason), body) == nil
	if code == coreconsensus.CodeStorageFailed {
		if err := os.MkdirAll(l.runDir, 0o700); err == nil {
			if writeFileSync(l.runtimeMarker(reason), body) == nil {
				written = true
			}
		}
	}
	return written
}

func reasonOf(code errs.Code) string {
	switch code {
	case coreconsensus.CodeStorageFailed:
		return "STORAGE_FAILED"
	case coreconsensus.CodeStateMachineFailed:
		return "STATE_MACHINE_FAILED"
	case coreconsensus.CodeVersionUnsupported:
		return "VERSION_UNSUPPORTED"
	case coreconsensus.CodeLogQuarantined:
		return "LOG_QUARANTINED"
	}
	return "STOPPED"
}

// checkMarkers refuses a start while a marker applies (D4): a
// STORAGE_FAILED marker of the CURRENT boot in either place, or any other
// stop marker, which only an operator clears.
func (l *FileLog) checkMarkers() error {
	if l.refuse != nil {
		return l.refuse
	}
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return storageFailed("markers", err)
	}
	boot := "boot=" + bootID() + "\n"
	paths := []string{l.runtimeMarker("STORAGE_FAILED")}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), markerStopPrefix) && !strings.HasSuffix(e.Name(), ".tmp") {
			paths = append(paths, filepath.Join(l.dir, e.Name()))
		}
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.HasSuffix(p, "STORAGE_FAILED") && !strings.HasPrefix(string(b), boot) {
			// Another boot: the page cache that may have lied is gone.
			continue
		}
		return origin(coreconsensus.StopMarkerPresent, errs.String("marker", filepath.Base(p)),
			errs.String("detail", strings.TrimSpace(string(b))))
	}
	return nil
}

// audit appends one line to the operator audit file and syncs it before the
// operation it records runs. Its Close is part of the write: a failure there
// is a storage failure like the sync's, never a detail dropped at return.
func (d DataDir) audit(op coreconsensus.Operator, what string) (err error) {
	f, err := os.OpenFile(filepath.Join(d.d.dir, fileAudit), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return storageFailed("audit", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = storageFailed("audit", cerr)
		}
	}()
	if _, err := fmt.Fprintf(f, "%s %s\n", op, what); err != nil {
		return storageFailed("audit", err)
	}
	if err := f.Sync(); err != nil {
		return storageFailed("audit", err)
	}
	return nil
}
