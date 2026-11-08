//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec

import (
	"os"
	"strconv"
	"syscall"
)

// oPath is O_PATH (Linux 2.6.39), 010000000 on every architecture this file
// builds for; the stdlib exports no name for it. A directory opened so is a
// position in the tree, not a read: walking needs search permission only.
const oPath int = 0x200000

// heldDir is one directory a PID-file walk stands in, held O_PATH: its
// entries are examined through /proc/self/fd/<fd>/<name>, so resolution starts
// at the directory held, never at a path a rename could move.
type heldDir struct {
	// fd is the O_PATH descriptor.
	fd int
}

// heldEntry is the walk's last component held O_PATH|O_NOFOLLOW: examined by
// fstat before anything is opened for reading.
type heldEntry struct {
	// fd is the O_PATH descriptor; the entry itself when it is a link.
	fd int
}

// openWalkRoot opens root as a walk's first directory, with its stat.
func openWalkRoot(root string) (heldDir, entryStat, error) {
	fd, err := syscall.Open(root, oPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	//: no root.
	if err != nil {
		//: the kernel's word.
		return heldDir{}, entryStat{}, err
	}
	st, err := fstatEntry(fd)
	//: a root without a stat.
	if err != nil {
		swallowErr(syscall.Close(fd))
		//: the kernel's word.
		return heldDir{}, entryStat{}, err
	}
	//: the held root.
	return heldDir{fd: fd}, st, nil
}

// lstat examines the entry name of d without following a link.
func (d heldDir) lstat(name string) (entryStat, error) {
	var st syscall.Stat_t
	//: through the held directory's magic link.
	if err := syscall.Lstat(procFDPath(d.fd, name), &st); err != nil {
		//: no such entry, or no search permission.
		return entryStat{}, err
	}
	//: what the walk judges.
	return statOf(&st), nil
}

// readlink reads the symbolic link name of d.
func (d heldDir) readlink(name string) (string, error) {
	//: the target, read through the held directory.
	return os.Readlink(procFDPath(d.fd, name))
}

// openDir holds the directory name of d, O_PATH and without following a link
// — one swapped in since the examination is refused by the kernel.
func (d heldDir) openDir(name string) (heldDir, entryStat, error) {
	fd, err := syscall.Openat(d.fd, name, oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	//: refused by the kernel: gone, or replaced by a link.
	if err != nil {
		//: the kernel's word.
		return heldDir{}, entryStat{}, err
	}
	st, err := fstatEntry(fd)
	//: no stat to prove the descent by.
	if err != nil {
		swallowErr(syscall.Close(fd))
		//: the entry cannot be proven.
		return heldDir{}, entryStat{}, errorOr(err, syscall.ESTALE)
	}
	//: the held directory and what it is.
	return heldDir{fd: fd}, st, nil
}

// entry holds the last component name of d as a position (O_PATH,
// O_NOFOLLOW): a link at the end, a FIFO or a device is examined without
// being opened at all.
func (d heldDir) entry(name string) (e heldEntry, st entryStat, why string, err error) {
	fd, err := syscall.Openat(d.fd, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	//: no such file, or no search permission on the way.
	if err != nil {
		//: the cause, kept matchable.
		return heldEntry{}, entryStat{}, "cannot be opened", err
	}
	st, err = fstatEntry(fd)
	//: the entry's type, owner and mode, from the position itself.
	if err != nil {
		swallowErr(syscall.Close(fd))
		//: the kernel's word.
		return heldEntry{}, entryStat{}, "cannot be examined", err
	}
	//: the held entry and what it is.
	return heldEntry{fd: fd}, st, "", nil
}

// releaseDirs releases every directory of a walk. It is kept out of line:
// inlined, the loop would push pidWalk.close past the inline budget, which a
// walk's one close does not need it to be in.
//
//go:noinline
func releaseDirs(dirs []heldDir) {
	//: each O_PATH descriptor.
	for _, d := range dirs {
		swallowErr(syscall.Close(d.fd))
	}
}

// close releases the held directory.
func (d heldDir) close() {
	//: the O_PATH descriptor.
	swallowErr(syscall.Close(d.fd))
}

// read reopens the held entry for reading through its position, which
// reaches the very inode examined; path names the file.
func (e heldEntry) read(path string) (f *os.File, why string, err error) {
	rfd, err := syscall.Open(procFDPath(e.fd, ""), syscall.O_RDONLY|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	//: a file this process may not read.
	if err != nil {
		//: the cause.
		return nil, "cannot be read", err
	}
	//: the open file.
	return os.NewFile(uintptr(rfd), path), "", nil
}

// close releases the held entry.
func (e heldEntry) close() {
	//: the O_PATH descriptor.
	swallowErr(syscall.Close(e.fd))
}

// fstatEntry is fstat(2) of fd, as the walk reads it.
func fstatEntry(fd int) (entryStat, error) {
	var st syscall.Stat_t
	//: the kernel's stat of what fd holds.
	if err := syscall.Fstat(fd, &st); err != nil {
		//: the kernel's word.
		return entryStat{}, err
	}
	//: what the walk judges.
	return statOf(&st), nil
}

// statOf is what the walk reads of a Linux stat.
func statOf(st *syscall.Stat_t) entryStat {
	//: the inode, the owner, the mode.
	return entryStat{dev: st.Dev, ino: st.Ino, uid: st.Uid, mode: st.Mode}
}

// procFDPath names entry name in the directory held as fd, through
// /proc/self/fd: resolution starts at that directory, not at a path. An empty
// name names what fd holds.
func procFDPath(fd int, name string) string {
	link := "/proc/self/fd/" + strconv.Itoa(fd)
	//: the descriptor itself.
	if name == "" {
		//: the magic link alone.
		return link
	}
	//: the magic link, then the entry.
	return link + "/" + name
}
