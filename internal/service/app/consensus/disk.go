package consensus

import (
	"io"
	"os"
	"path/filepath"
)

// segFile is what the log and the snapshot store write a file through: an
// *os.File in production, the fault-injecting wrapper of D15's shim lane
// under a test. Reads that only judge what is on disk (recovery's scan, a
// snapshot's Open) open their file with os.Open and are not part of it.
type segFile interface {
	io.ReaderAt
	io.WriterAt
	io.Writer
	io.Seeker
	Stat() (os.FileInfo, error)
	Truncate(size int64) error
	Sync() error
	Close() error
	Name() string
}

// diskFS is the one fault seam of the data directory (ADR 0152 D15): every
// write, sync, rename and removal the log, the snapshot store, the stamp,
// the markers and the offline operations make goes through the function
// fields of the diskFS their dataDir holds, and through the segFile its
// openFile returns. Production is osDisk, the os functions; the shim lane's
// faultFS counts each operation, fails the one it is told to, or stops the
// process model there and reopens what was written but not synced
// (TestEveryFaultPoint). The tests that need a moment of a write — between
// a batch's write and its fdatasync, after a seal is durable — read it off
// the operations this seam sees, never off a hook of the log's own.
type diskFS struct {
	// openFile opens name to be written, as os.OpenFile does.
	openFile func(name string, flag int, perm os.FileMode) (segFile, error)
	// datasync makes f's written bytes durable: fdatasync(2) on Linux,
	// os.File.Sync elsewhere (datasync).
	datasync func(f segFile) error
	// syncDir makes a directory's entries durable (syncDir).
	syncDir func(dir string) error
	// rename and remove are os.Rename and os.Remove; removeAll is
	// os.RemoveAll.
	rename    func(from, to string) error
	remove    func(name string) error
	removeAll func(path string) error
	// background runs the zero-fill of the next segment off the writer's
	// path: a goroutine in production, at once under the shim, so that an
	// operation's index names the same operation at every run.
	background func(run func())
}

// osDisk is the production seam: the os functions.
var osDisk = diskFS{
	openFile:   openOSFile,
	datasync:   datasyncFile,
	syncDir:    syncDir,
	rename:     os.Rename,
	remove:     os.Remove,
	removeAll:  os.RemoveAll,
	background: func(run func()) { go run() },
}

// openOSFile is os.OpenFile as a segFile; a failed open is a nil interface,
// never a typed nil.
//
// IFACE-PLUGIN: segFile is the disk fault seam (ADR 0152 D15): an *os.File in
// production, the fault-injecting wrapper under test, so the result cannot be
// one concrete type.
func openOSFile(name string, flag int, perm os.FileMode) (segFile, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// datasyncFile is datasync on the *os.File under f, and f's own Sync on any
// other segFile.
func datasyncFile(f segFile) error {
	if of, ok := f.(*os.File); ok {
		return datasync(of)
	}
	return f.Sync()
}

// writeSynced writes data to a fresh file at path and syncs it. The file is
// closed before it returns, so before writeFileSync renames it, and a Close
// failure is reported like the sync's: it is part of the write.
func (d *diskFS) writeSynced(path string, data []byte) (err error) {
	f, err := d.openFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

// writeFileSync publishes data at path atomically and durably: written and
// synced to path.tmp, renamed over path, and the directory synced.
func (d *diskFS) writeFileSync(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := d.writeSynced(tmp, data); err != nil {
		return err
	}
	if err := d.rename(tmp, path); err != nil {
		return err
	}
	return d.syncDir(filepath.Dir(path))
}

// mkdirDurable creates dir, private to the owner, and when it did not exist
// syncs its parent: the new entry is otherwise published only by whatever
// syncs that directory next, which ext4 and XFS do through their journal and
// other filesystems may not (#422). A directory already there is left as it
// is, so a parent this process may not open is never asked for.
func (d *diskFS) mkdirDurable(dir string) error {
	_, err := os.Stat(dir)
	existed := err == nil
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return storageFailed("mkdir", err)
	}
	if existed {
		return nil
	}
	if err := d.syncDir(filepath.Dir(dir)); err != nil {
		return storageFailed("mkdir", err)
	}
	return nil
}

// zeroFrom zeroes f from off to size, 1 MiB at a time, and syncs it; the
// sync runs even when there is nothing to zero, so the caller may rely on
// the range being durable zeros on return.
func (d *diskFS) zeroFrom(f segFile, off, size int64) error {
	if err := zeroRange(f, off, size); err != nil {
		return err
	}
	return d.datasync(f)
}

// abandonTmp closes f, the segment .tmp at path a failure leaves
// unpublished, removes it, and returns cause, the failure that stops the
// log. The .tmp is never renamed, so what its Close says changes nothing. It
// is removed now, not at the next start: a zero-fill that failed on a full
// disk wrote until the disk had no byte left, and the disk is shared with
// what the product stores beside the log (#520). A removal that fails
// leaves it to recovery, which deletes every .tmp.
func (d *diskFS) abandonTmp(f io.Closer, path string, cause error) error {
	abandon(f.Close())
	abandon(d.remove(path))
	return cause
}
