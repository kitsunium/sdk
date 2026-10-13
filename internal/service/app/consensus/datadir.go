package consensus

import (
	"bytes"
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
	// fileLock is the file whose flock excludes a second process (D11).
	fileLock = "LOCK"
	// fileStamp holds the node's identity and its log's lineage.
	fileStamp = "STAMP"
	// markerQuarantine is written FIRST when recovery finds damage it may not
	// repair; while it exists the log refuses every call (D4).
	markerQuarantine = "QUARANTINE"
	// markerStopPrefix starts the name of every stop marker, the reason
	// following it: a start is refused while one applies (D4, R9-05).
	markerStopPrefix = "STOP-"

	// stampVersion is the first byte of an encoded stamp; a stamp of another
	// version is unreadable, never guessed at.
	stampVersion byte = 1
	// stampFixed is the stamp's length before its slot and its CRC: version
	// 1, cluster 16, epoch 8, predecessor 16, predecessor epoch 8, kept
	// index 8, id 8, incarnation 16, state 1, founding 32, slot length 1.
	stampFixed int = 1 + 16 + 8 + 16 + 8 + 8 + 8 + 16 + 1 + 32 + 1
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
		return DataDir{}, alsoFailed(storageFailed("lock", err), "close", lf.Close())
	}
	if !held {
		return DataDir{}, alsoFailed(origin(coreconsensus.DataDirLocked, errs.String("dir", dir)), "close", lf.Close())
	}
	if err := d.open(); err != nil {
		// Closing the descriptor releases the lock too; the explicit unlock
		// is what makes the release immediate on Windows, and its failure
		// travels with the cause rather than in its place.
		err = alsoFailed(err, "unlock", flock.Unlock(lf))
		return DataDir{}, alsoFailed(err, "close", lf.Close())
	}
	return DataDir{d: d}, nil
}

// open opens the log, then the snapshots; when the snapshots fail the log
// just opened is closed again, so a failed open holds nothing but the lock
// its caller releases.
func (d *dataDir) open() error {
	l, err := openFileLog(d, d.opts.SegmentBytes)
	if err != nil {
		return err
	}
	s, err := newFileSnapshots(filepath.Join(d.dir, "snap"))
	if err != nil {
		return alsoFailed(err, "close log", l.Close())
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

// acquire marks the data directory held by a Node, refusing DATA_DIR_IN_USE
// while another Node holds it or it was closed; a log opened without a
// DataDir holds nothing and always succeeds.
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

// release ends the hold acquire took, so the directory may be closed or
// operated on offline again.
func (l *FileLog) release() {
	if l.dd == nil {
		return
	}
	l.dd.mu.Lock()
	l.dd.inUse = false
	l.dd.mu.Unlock()
}

// ---- stamp ----

// encodeStamp is the stamp's on-disk form: the fixed fields, the slot
// prefixed by its length, and a CRC-32C of everything before it.
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

// decodeStamp parses encodeStamp's form; ok is false for another version, a
// short stamp, a CRC that fails, a state this version does not define, or a
// slot length that disagrees with the bytes left.
func decodeStamp(b []byte) (coreconsensus.Stamp, bool) {
	var s coreconsensus.Stamp
	if len(b) < stampFixed+4 || b[0] != stampVersion {
		return s, false
	}
	body := b[:len(b)-4]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
		return s, false
	}
	p := 1
	copy(s.Log.Cluster[:], body[p:])
	p += len(s.Log.Cluster)
	s.Log.Epoch = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	copy(s.Log.Predecessor[:], body[p:])
	p += len(s.Log.Predecessor)
	s.Log.PredecessorEpoch = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	s.Log.KeptIndex = binary.LittleEndian.Uint64(body[p:])
	p += u64Bytes
	s.ID = coreconsensus.NodeID(binary.LittleEndian.Uint64(body[p:]))
	p += u64Bytes
	copy(s.Incarnation[:], body[p:])
	p += len(s.Incarnation)
	s.State = coreconsensus.StampState(body[p])
	if s.State < coreconsensus.StampJoining || s.State > coreconsensus.StampMember {
		// A state this version does not define is a corrupt or a newer
		// stamp: unreadable, never a directory Bootstrap may found.
		return coreconsensus.Stamp{}, false
	}
	p++
	copy(s.Founding[:], body[p:])
	p += len(s.Founding)
	n := int(body[p])
	p++
	if p+n != len(body) {
		return s, false
	}
	s.Slot = string(body[p:])
	return s, true
}

// readStamp reads the stamp of dir: none yet is the zero stamp, a read that
// fails is a STORAGE_FAILED, and a stamp that cannot be decoded is
// DATA_DIR_MISMATCH, since nothing in it can be trusted to name this node.
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

// writeStamp replaces the stamp of dir atomically and durably.
func writeStamp(dir string, s coreconsensus.Stamp) error {
	return writeFileSync(filepath.Join(dir, fileStamp), encodeStamp(s))
}

// ---- stop markers (D4, R7-14, R8-05, R9-05) ----

// runtimeMarker is the boot-scoped path of the log's marker for reason,
// named by a digest of the data directory so two directories on one host
// never share a marker.
func (l *FileLog) runtimeMarker(reason string) string {
	sum := sha256.Sum256([]byte(l.dir))
	return filepath.Join(l.runDir, hex.EncodeToString(sum[:8])+"-"+reason)
}

// markStopEach writes the stop's marker: STORAGE_FAILED in TWO places, the
// data directory and a boot-scoped path, each holding the boot id; the final
// stops in the data directory only. It returns the failure of each place:
// the data directory's, and the boot-scoped one's (nil for a stop that writes no
// boot-scoped marker). R9-05 lets a node stop with one marker of the two,
// but not silently: the caller reports the place it could not write.
func (l *FileLog) markStopEach(code errs.Code, detail string) (dirErr, runErr error) {
	reason := reasonOf(code)
	body := []byte(fmt.Sprintf("boot=%s\n%s\n", bootID(), detail))
	dirErr = writeFileSync(filepath.Join(l.dir, markerStopPrefix+reason), body)
	if code != coreconsensus.CodeStorageFailed {
		return dirErr, nil
	}
	if runErr = os.MkdirAll(l.runDir, 0o700); runErr == nil {
		runErr = writeFileSync(l.runtimeMarker(reason), body)
	}
	return dirErr, runErr
}

// reasonOf is the reason a stop marker's name carries for code.
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
	default:
		// Any other final stop still leaves a marker an operator must clear;
		// it is named generically rather than not written.
		return "STOPPED"
	}
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
	boot := []byte("boot=" + bootID() + "\n")
	for _, p := range l.markerPaths(ents) {
		b, err := os.ReadFile(p)
		if errors.Is(err, os.ErrNotExist) {
			// No marker there: the boot-scoped one is absent on every start
			// that followed no storage failure.
			continue
		}
		if err != nil {
			// A marker that is there but cannot be read is a stop whose
			// reason is unknown, never an absent one: the start is refused.
			return storageFailed("read stop marker", err)
		}
		if strings.HasSuffix(p, "STORAGE_FAILED") && !bytes.HasPrefix(b, boot) {
			// Another boot: the page cache that may have lied is gone.
			continue
		}
		return stopMarkerPresent(p, b)
	}
	return nil
}

// stopMarkerPresent is the STOP_MARKER_PRESENT of the marker at path, its
// trimmed content as the detail an operator reads before clearing it.
func stopMarkerPresent(path string, content []byte) error {
	return origin(coreconsensus.StopMarkerPresent, errs.String("marker", filepath.Base(path)),
		errs.String("detail", strings.TrimSpace(string(content))))
}

// markerPaths is every place a stop marker of this log may be: the
// boot-scoped STORAGE_FAILED marker first, then each published STOP- file of
// the data directory listing ents, a half-written .tmp excluded.
func (l *FileLog) markerPaths(ents []os.DirEntry) []string {
	paths := []string{l.runtimeMarker("STORAGE_FAILED")}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), markerStopPrefix) && !strings.HasSuffix(e.Name(), ".tmp") {
			paths = append(paths, filepath.Join(l.dir, e.Name()))
		}
	}
	return paths
}
