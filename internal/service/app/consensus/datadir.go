package consensus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
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
)

const (
	// PreallocateZeroFill writes zeros over a new segment's full size, then
	// syncs it. It is the zero value and the default (D4).
	PreallocateZeroFill Preallocate = iota
	// PreallocateFallocate reserves a new segment's full size with
	// fallocate(2) on Linux, its blocks unwritten extents that read as zeros,
	// then syncs it: no zero is written. A filesystem or a platform without
	// it opens the log in PreallocateZeroFill, with one line logged (#922).
	PreallocateFallocate
)

const (
	// SyncFdatasync writes a batch's records with pwrite(2), then makes them
	// durable with fdatasync(2): two calls per durability point. It is the
	// zero value and the default (D4, #417).
	SyncFdatasync SyncPath = iota
	// SyncRWFDsync writes a batch's records with pwritev2(2) flagged
	// RWF_DSYNC on Linux, which returns once they are durable: one call per
	// durability point, the same two points as SyncFdatasync. A kernel or a
	// platform without it opens the log in SyncFdatasync, with one line
	// logged (#923).
	SyncRWFDsync
)

// Preallocate is how the file log gives a new segment its fixed size
// before it holds a record (D4, #922).
type Preallocate uint8

// SyncPath is how the file log makes a batch's records durable (D4,
type SyncPath uint8

// DataDirOptions tunes NewDataDirWith: the size of a log segment, how a
// new one is preallocated, how a batch is made durable, and the
// boot-scoped directory of the second STORAGE_FAILED marker.
type DataDirOptions struct {
	// SegmentBytes is the fixed size of a log segment; 0 is 64 MiB.
	SegmentBytes int64
	// Preallocate is how a new segment is given its fixed size: zeros
	// written (PreallocateZeroFill, the zero value) or fallocate(2)
	// (PreallocateFallocate). Either way the segment occupies its whole
	// size, as the no-space guard counts it. Another value is refused
	// CONSENSUS_MISCONFIGURED.
	Preallocate Preallocate
	// SyncPath is how a batch's records are made durable: pwrite(2) then
	// fdatasync(2) (SyncFdatasync, the zero value) or one pwritev2(2)
	// flagged RWF_DSYNC (SyncRWFDsync). Either way a batch has the same two
	// durability points, its end record written once the rest is durable.
	// Another value is refused CONSENSUS_MISCONFIGURED.
	SyncPath SyncPath
	// RuntimeDir is the boot-scoped directory of the second STORAGE_FAILED
	// marker (R9-05); empty is /run/kitsunium-consensus on Linux, and
	// kitsunium-consensus in the temporary directory elsewhere. It must
	// outlive the process within the boot and be gone at the next one: a
	// host tmpfs, or systemd's RuntimeDirectory= with
	// RuntimeDirectoryPreserve=yes. The process must be able to create it
	// and write in it: NewDataDirWith refuses CONSENSUS_MISCONFIGURED
	// otherwise, naming the path.
	RuntimeDir string
}

type dataDir struct {
	dir   string
	opts  DataDirOptions
	mu    sync.Mutex
	lockf *os.File
	// disk is the fault seam the log, the snapshot store and the offline
	// operations write through: osDisk, a test's faultFS (fs).
	disk   *diskFS
	log    *fileLog
	snaps  *fileSnapshots
	closed bool
	inUse  bool
	// afterTruncate runs once TruncateAndStaySilent's truncation is
	// durable, before the quarantine marker goes; an error ends the exit
	// there, as a crash would. Only a test sets it.
	afterTruncate func() error
	// afterWipeStep runs after each step of WipeAndRejoin, named: "marker"
	// once STOP-WIPE is durable, "log" and "snap" once each store is
	// removed, "stamp" once the stamp and the other markers are durably
	// gone, "done" once STOP-WIPE is; an error ends the wipe there, as a
	// crash would. Only a test sets it.
	afterWipeStep func(step string) error
}

// DataDir is the shipped layout: a lock, a stamp, the segmented log, the
// snapshots and the markers. A DataDir value wraps a pointer, so copies
// share one lock and one Close (R4-29).
type DataDir struct{ d *dataDir }

// NewDataDir takes the directory lock FIRST, then reads, scans and repairs
// (D11). A second process fails at once with DATA_DIR_LOCKED.
func NewDataDir(dir string) (DataDir, error) { return NewDataDirWith(dir, DataDirOptions{}) }

// NewDataDirWith is NewDataDir with options.
//
//go:noinline
func NewDataDirWith(dir string, opts DataDirOptions) (DataDir, error) {
	// Kept out of line (go:noinline): inlined, this call took NewDataDir
	// and the facade's forwarder past their inline budget of 80 (kit gen).
	return newDataDirOn(dir, opts, &osDisk)
}

// newDataDirOn is NewDataDirWith over the fault seam disk: osDisk in
// production, a test's faultFS under the shim lane (D15).
func newDataDirOn(dir string, opts DataDirOptions, disk *diskFS) (DataDir, error) {
	if err := refuseUnmarked(dir); err != nil {
		return DataDir{}, err
	}
	if opts.Preallocate > PreallocateFallocate {
		return DataDir{}, misconfigured("Preallocate", "not a Preallocate mode")
	}
	if opts.SyncPath > SyncRWFDsync {
		return DataDir{}, misconfigured("SyncPath", "not a SyncPath mode")
	}
	// The directory lock is the first thing taken (D11), and there is none to
	// take where the kernel has no flock(2) — illumos and Solaris (ADR 0144):
	// the refusal is the platform's, at construction and before anything is
	// written (ADR 0018 §(a)), never a storage failure an operator would
	// chase on the disk.
	if !flock.Native {
		return DataDir{}, coreproc.UnsupportedPlatform
	}
	if err := disk.mkdirDurable(dir); err != nil {
		return DataDir{}, err
	}
	d := &dataDir{dir: dir, opts: opts, disk: disk}
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
// its caller releases. A failed sync of the snapshot directory writes the
// log's markers, as the log's own syncs at open do (#637).
func (d *dataDir) open() error {
	l, err := openFileLog(d, d.opts.SegmentBytes)
	if err != nil {
		return err
	}
	s, err := newFileSnapshotsOn(filepath.Join(d.dir, "snap"), l.syncFailedAtOpen, d.fs())
	if err != nil {
		return alsoFailed(err, "close log", l.Close())
	}
	d.log, d.snaps = l, s
	return nil
}

// fs is the directory's fault seam, osDisk for a dataDir built without
// one.
func (d *dataDir) fs() *diskFS {
	if d.disk != nil {
		return d.disk
	}
	return &osDisk
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
func (l *fileLog) acquire() error {
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
func (l *fileLog) release() {
	if l.dd == nil {
		return
	}
	l.dd.mu.Lock()
	l.dd.inUse = false
	l.dd.mu.Unlock()
}

// ---- stop markers (D4, R7-14, R8-05, R9-05) ----

// runtimeMarker is the boot-scoped path of the log's marker for reason,
// named by a digest of the data directory so two directories on one host
// never share a marker.
func (l *fileLog) runtimeMarker(reason string) string {
	sum := sha256.Sum256([]byte(l.dir))
	return filepath.Join(l.runDir, hex.EncodeToString(sum[:8])+"-"+reason)
}

// markStopEach writes the stop's marker: STORAGE_FAILED in TWO places, the
// data directory and a boot-scoped path, each holding the boot id; the final
// stops in the data directory only. It returns the failure of each place:
// the data directory's, and the boot-scoped one's (nil for a stop that writes no
// boot-scoped marker). R9-05 lets a node stop with one marker of the two,
// but not silently: the caller reports the place it could not write. The
// boot-scoped marker is written first: it sits on a tmpfs, refuses a
// restart in this boot alone, and a data directory whose device hangs or
// refuses the write cannot keep it from being written (#637).
func (l *fileLog) markStopEach(code errs.Code, detail string) (dirErr, runErr error) {
	reason := reasonOf(code)
	body := fmt.Appendf(nil, "boot=%s\n%s\n", bootID(), detail)
	if code == coreconsensus.CodeStorageFailed {
		if runErr = os.MkdirAll(l.runDir, 0o700); runErr == nil {
			runErr = l.fs().writeFileSync(l.runtimeMarker(reason), body)
		}
	}
	dirErr = l.fs().writeFileSync(filepath.Join(l.dir, markerStopPrefix+reason), body)
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
func (l *fileLog) checkMarkers() error {
	if l.refuse != nil {
		return l.refuse
	}
	return l.markerApplies(false)
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
func (l *fileLog) markerPaths(ents []os.DirEntry) []string {
	paths := []string{l.runtimeMarker("STORAGE_FAILED")}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), markerStopPrefix) && !strings.HasSuffix(e.Name(), ".tmp") {
			paths = append(paths, filepath.Join(l.dir, e.Name()))
		}
	}
	return paths
}
