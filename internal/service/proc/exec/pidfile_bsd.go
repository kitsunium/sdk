//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"syscall"
)

// heldDir is one directory a PID-file walk stands in, held as an os.Root:
// its entries are examined and opened relative to the descriptor
// (fstatat(2), readlinkat(2), openat(2) with O_NOFOLLOW — the stdlib reaches
// them on every one of these kernels, OpenBSD's and darwin's libc included),
// never through a path a rename could move. These kernels have no O_PATH, so
// a directory is held open for reading: the walk needs read permission on
// each, where Linux needs search permission only.
type heldDir struct {
	// root is the open directory.
	root *os.Root
}

// heldEntry is the walk's last component, examined by lstat and read by name
// in its directory once its type allows it.
type heldEntry struct {
	// dir is the directory that holds it.
	dir *os.Root
	// name is its name there.
	name string
	// st is what the examination read: the read must reach that inode.
	st entryStat
}

// openWalkRoot opens root as a walk's first directory, with its stat.
func openWalkRoot(root string) (heldDir, entryStat, error) {
	r, err := os.OpenRoot(root)
	//: no root.
	if err != nil {
		//: the cause.
		return heldDir{}, entryStat{}, err
	}
	d := heldDir{root: r}
	st, err := d.lstat(".")
	//: a root without a stat.
	if err != nil {
		d.close()
		//: the cause.
		return heldDir{}, entryStat{}, err
	}
	//: the held root.
	return d, st, nil
}

// lstat examines the entry name of d without following a link; "." is d.
func (d heldDir) lstat(name string) (entryStat, error) {
	info, err := d.root.Lstat(name)
	//: no such entry, or no search permission.
	if err != nil {
		//: the cause, kept matchable.
		return entryStat{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	//: a stat this kernel did not give.
	if !ok {
		//: nothing to judge by.
		return entryStat{}, syscall.EINVAL
	}
	//: what the walk judges.
	return statOf(st), nil
}

// readlink reads the symbolic link name of d.
func (d heldDir) readlink(name string) (string, error) {
	//: readlinkat(2) relative to d.
	return d.root.Readlink(name)
}

// openDir holds the directory name of d. os.Root opens it O_NOFOLLOW and,
// should a link have been swapped in, resolves it only inside d: the walk
// then sees another inode than the one it examined and refuses the descent.
func (d heldDir) openDir(name string) (heldDir, entryStat, error) {
	r, err := d.root.OpenRoot(name)
	//: gone, not a directory, or not readable.
	if err != nil {
		//: the cause.
		return heldDir{}, entryStat{}, err
	}
	sub := heldDir{root: r}
	st, err := sub.lstat(".")
	//: no stat to prove the descent by.
	if err != nil {
		sub.close()
		//: the entry cannot be proven.
		return heldDir{}, entryStat{}, errorOr(err, syscall.ESTALE)
	}
	//: the held directory and what it is.
	return sub, st, nil
}

// entry examines the last component name of d without following a link and
// without opening it: a link at the end, a FIFO or a device is judged by its
// type before anything reads it.
func (d heldDir) entry(name string) (e heldEntry, st entryStat, why string, err error) {
	st, err = d.lstat(name)
	//: no such file, or no search permission on the way.
	if err != nil {
		//: the cause, kept matchable (fs.ErrNotExist).
		return heldEntry{}, entryStat{}, "cannot be opened", err
	}
	//: the entry, by name in its held directory.
	return heldEntry{dir: d.root, name: name, st: st}, st, "", nil
}

// releaseDirs releases every directory of a walk. It is kept out of line:
// inlined, the loop would push pidWalk.close past the inline budget, which a
// walk's one close does not need it to be in.
//
//go:noinline
func releaseDirs(dirs []heldDir) {
	//: each held directory's descriptor.
	for _, d := range dirs {
		swallowErr(d.root.Close())
	}
}

// close releases the held directory.
func (d heldDir) close() {
	//: its descriptor.
	swallowErr(d.root.Close())
}

// read opens the entry for reading — O_NONBLOCK, so a FIFO swapped in since
// the examination does not block the open — and proves the file opened is
// the inode examined. path names the file in the walk's errors, which the
// os.Root's own error already names relative to its directory.
func (e heldEntry) read(_ string) (f *os.File, why string, err error) {
	f, err = e.dir.OpenFile(e.name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	//: a file this process may not read.
	if err != nil {
		//: the cause.
		return nil, "cannot be read", err
	}
	info, err := f.Stat()
	got, ok := statFromInfo(info, err)
	//: not the inode examined: a rename or a link swapped in meanwhile.
	if !ok || got.dev != e.st.dev || got.ino != e.st.ino {
		swallowErr(f.Close())
		//: refused, never read.
		return nil, "replaced while it was opened", errorOr(err, syscall.ESTALE)
	}
	//: the file examined, open.
	return f, "", nil
}

// close releases nothing: the entry is held by name in a directory the walk
// closes.
func (e heldEntry) close() {
	//: the directory's descriptor is the walk's.
}

// statFromInfo is what the walk reads of an os.FileInfo, false when there is
// none.
func statFromInfo(info os.FileInfo, err error) (entryStat, bool) {
	//: no stat.
	if err != nil {
		//: nothing to compare.
		return entryStat{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	//: a stat this kernel did not give.
	if !ok {
		//: nothing to compare.
		return entryStat{}, false
	}
	//: the inode, the owner, the mode.
	return statOf(st), true
}

// statOf is what the walk reads of a BSD or darwin stat, whose st_dev and
// st_mode widths differ from one kernel to the next.
func statOf(st *syscall.Stat_t) entryStat {
	//: the inode, the owner, the mode, widened.
	return entryStat{dev: uint64(st.Dev), ino: uint64(st.Ino), uid: st.Uid, mode: uint32(st.Mode)}
}
