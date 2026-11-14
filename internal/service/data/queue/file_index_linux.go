//go:build linux

package queue

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

// watchMask is what a state directory's watch asks the kernel for: an entry
// added (a publication's rename, a link, a creation) or gone (a rename away,
// an unlink), and the directory itself moving or vanishing. The directory
// must be one: a path that resolves to anything else is refused.
const watchMask uint32 = syscall.IN_CREATE | syscall.IN_MOVED_TO | syscall.IN_DELETE | syscall.IN_MOVED_FROM |
	syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_ONLYDIR

// lostMask is the events that end a watch's answer for its directory: the
// directory moved, was removed, or its filesystem unmounted, and the watch
// was taken off.
const lostMask uint32 = syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_IGNORED | syscall.IN_UNMOUNT

// addedMask and removedMask are the events that add or remove one entry.
const (
	addedMask   uint32 = syscall.IN_CREATE | syscall.IN_MOVED_TO
	removedMask uint32 = syscall.IN_DELETE | syscall.IN_MOVED_FROM
)

// maxDrainReads bounds the reads one look makes of the instance: a writer
// that outpaces the drain would otherwise keep a Receive reading. Past it the
// queue is left as it is and both directories are read whole, which finds
// every change the reads left behind — as an overflow does.
const maxDrainReads int = 64

// watchBufferBytes is the buffer one read of the instance fills: a hundred
// and more events of the names this broker writes, and room for the longest
// name the kernel can queue, which a smaller buffer would refuse.
const watchBufferBytes int = 16 << 10

// The layout of the kernel's records, fixed by its ABI on every architecture:
// struct inotify_event, and struct linux_dirent64 as getdents64 writes it —
// the system call syscall.ReadDirent makes on every Linux.
const (
	eventMaskAt   int = 4
	eventLenAt    int = 12
	direntReclen  int = 16
	direntNameAt  int = 19
	inotifyHeader int = syscall.SizeofInotifyEvent
)

// remoteMagics is the filesystems a watch cannot vouch for: another host
// changes them, and inotify sees only what this kernel did. Each is its
// f_type, as statfs(2) reports it.
var remoteMagics = [...]uint32{
	0x6969,     // NFS
	0x517b,     // SMB
	0xff534d42, // CIFS
	0xfe534d42, // SMB2
	0x65735546, // FUSE: sshfs, s3fs, rclone, gcsfuse…
	0x00c36400, // Ceph
	0x73757245, // Coda
	0x5346414f, // AFS
	0x6b414653, // kAFS
	0x01021997, // 9P
	0x564c,     // NCP
	0x01161970, // GFS2
	0x7461636f, // OCFS2
	0x0bd00bd0, // Lustre
	0x786f4256, // VirtualBox shared folders
	0x7c7c6673, // Parallels shared folders
	0x6a656a63, // virtiofs
}

// stateWatch is one inotify instance watching a broker's ready/ and
// inflight/, read without blocking.
type stateWatch struct {
	// file owns the instance's descriptor: it closes it, and closes it once
	// garbage collected when a broker was never closed.
	file *os.File
	// raw reads the descriptor through file, without the runtime's poller
	// waiting on it.
	raw syscall.RawConn
	// readErr is the last read's refusal.
	readErr error
	// read is readOnce, bound once so a look allocates no closure.
	read func(uintptr) bool
	// buf is what a read fills.
	buf [watchBufferBytes]byte
	// filled is how much the last read filled.
	filled int
	// wds is each watched directory's watch descriptor, in states' order.
	wds [2]int32
	// states is the two watched directories.
	states [2]string
}

// watchStates watches root's ready/ and inflight/, and returns why it could
// not instead: no instance left (EMFILE past max_user_instances), no /proc to
// name an opened directory by, a remote or FUSE filesystem.
func watchStates(root dirOpener) (watch *stateWatch, why string) {
	descriptor, initErr := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	//: no instance: a kernel without inotify, or the user's limit reached.
	if initErr != nil {
		//: read under the listings' rule.
		return nil, "inotify_init1: " + initErr.Error()
	}
	file := os.NewFile(uintptr(descriptor), "inotify")
	watch = &stateWatch{file: file, states: [2]string{dirReady, dirInflight}}
	//: each state directory, as the root opens it.
	for index, state := range watch.states {
		wd, refused := watchState(root, descriptor, state)
		//: one of the two cannot be watched: neither is.
		if refused != "" {
			//: read under the listings' rule, the instance released.
			return nil, closedWhy(file, refused)
		}
		watch.wds[index] = wd
	}
	raw, rawErr := file.SyscallConn()
	//: an instance this process cannot read is no watch.
	if rawErr != nil {
		//: read under the listings' rule, the instance released.
		return nil, closedWhy(file, "inotify: "+rawErr.Error())
	}
	watch.raw, watch.read = raw, watch.readOnce
	//: watched.
	return watch, ""
}

// closedWhy closes held and returns why, with the close's refusal after it
// when there was one.
func closedWhy(held io.Closer, why string) string {
	//: the descriptor goes back whatever the refusal was.
	if closeErr := held.Close(); closeErr != nil {
		//: both said.
		return why + "; close: " + closeErr.Error()
	}
	//: the refusal alone.
	return why
}

// watchState adds a watch on root's state directory to the instance
// descriptor. The directory is the one root opens — named through
// /proc/self/fd, never by a path resolved again —, and its filesystem must be
// one whose every change this kernel makes.
func watchState(root dirOpener, descriptor int, state string) (wd int32, why string) {
	dir, openErr := root.Open(state)
	//: the directory the broker uses, or nothing.
	if openErr != nil {
		//: the look that follows reports it.
		return 0, "open " + state + ": " + openErr.Error()
	}
	wd, why = watchOpened(dir, descriptor, state)
	//: the directory's descriptor is only needed while the watch is added.
	return wd, closedWhy(dir, why)
}

// watchOpened adds a watch on the opened state directory dir to the instance
// descriptor, unless dir's filesystem is one another host changes.
func watchOpened(dir *os.File, descriptor int, state string) (wd int32, why string) {
	held := int(dir.Fd())
	defer runtime.KeepAlive(dir)
	var fs syscall.Statfs_t
	//: the filesystem's type decides whether a watch sees every change.
	if statErr := syscall.Fstatfs(held, &fs); statErr != nil {
		//: read under the listings' rule.
		return 0, "statfs " + state + ": " + statErr.Error()
	}
	//: another host writes there, and this kernel queues nothing for it.
	if magic := uint32(fs.Type); remoteFilesystem(magic) {
		//: read under the listings' rule.
		return 0, "a network or FUSE filesystem (f_type 0x" + strconv.FormatUint(uint64(magic), 16) + ")"
	}
	added, addErr := syscall.InotifyAddWatch(descriptor, "/proc/self/fd/"+strconv.Itoa(held), watchMask)
	//: no /proc, the watches' limit reached.
	if addErr != nil {
		//: read under the listings' rule.
		return 0, "inotify_add_watch " + state + ": " + addErr.Error()
	}
	//: watched.
	return int32(added), ""
}

// remoteFilesystem reports whether magic is the f_type of a filesystem
// another host or a user-space server changes behind this kernel.
func remoteFilesystem(magic uint32) bool {
	//: a short list, walked once a broker.
	for _, remote := range remoteMagics {
		//: named.
		if magic == remote {
			//: not watched.
			return true
		}
	}
	//: a local filesystem.
	return false
}

// readOnce reads the instance once, never waiting: it is the function raw
// runs, and it always reports done.
func (w *stateWatch) readOnce(descriptor uintptr) bool {
	w.filled, w.readErr = syscall.Read(int(descriptor), w.buf[:])
	//: one read, whatever it answered.
	return true
}

// drain applies to index every event the kernel queued, until the instance
// is empty. It reports lost when a watched directory moved or vanished, and
// the error of an instance that can no longer be read.
func (w *stateWatch) drain(index *fileIndex) (lost bool, err error) {
	//: until EAGAIN — the queue is empty —, maxDrainReads at most.
	for range maxDrainReads {
		//: a closed file.
		if rawErr := w.raw.Read(w.read); rawErr != nil {
			//: the watch is dropped.
			return false, rawErr
		}
		//: what the read answered.
		switch {
		//: empty.
		case w.readErr == syscall.EAGAIN:
			//: every change applied.
			return false, nil
		//: a signal: read again.
		case w.readErr == syscall.EINTR:
			continue
		//: anything else: the instance cannot be read.
		case w.readErr != nil:
			//: the watch is dropped.
			return false, w.readErr
		//: events.
		default:
			//: a directory gone: no event after it matters.
			if w.apply(index, w.buf[:w.filled]) {
				//: the watch is dropped.
				return true, nil
			}
		}
	}
	index.overflowed()
	//: what is left is found by the full reads it asked for.
	return false, nil
}

// apply hands each event of one read to index, in order, and reports
// whether one of them ended a watch's answer, which stops it.
func (w *stateWatch) apply(index *fileIndex, events []byte) bool {
	//: one record after another; a truncated one is no record.
	for len(events) >= inotifyHeader {
		wd := int32(binary.NativeEndian.Uint32(events))
		mask := binary.NativeEndian.Uint32(events[eventMaskAt:])
		end := inotifyHeader + int(binary.NativeEndian.Uint32(events[eventLenAt:]))
		//: a record past the read: the kernel never writes one.
		if end > len(events) {
			index.overflowed()
			//: read whole at the next look.
			return false
		}
		name := events[inotifyHeader:end]
		events = events[end:]
		//: a directory gone: no event after it matters.
		if w.applyOne(index, wd, mask, name) {
			//: dropped.
			return true
		}
	}
	//: applied.
	return false
}

// applyOne hands one event to index — its name still padded with NULs — and
// reports whether it ended a watch's answer.
func (w *stateWatch) applyOne(index *fileIndex, wd int32, mask uint32, name []byte) bool {
	//: the name ends at its first NUL.
	if cut := bytes.IndexByte(name, 0); cut >= 0 {
		name = name[:cut]
	}
	//: what the event says.
	switch {
	//: changes were lost: both directories are read whole.
	case mask&syscall.IN_Q_OVERFLOW != 0:
		index.overflowed()
	//: a directory moved or vanished: its watch no longer answers.
	case mask&lostMask != 0:
		//: dropped.
		return true
	//: an entry added — a directory in a state directory is no message.
	case mask&addedMask != 0 && mask&syscall.IN_ISDIR == 0:
		index.added(w.stateOf(wd), name)
	//: an entry gone.
	case mask&removedMask != 0:
		index.removed(w.stateOf(wd), name)
	//: nothing the index keeps.
	default:
	}
	//: applied.
	return false
}

// stateOf returns the state directory wd watches.
func (w *stateWatch) stateOf(wd int32) string {
	//: ready/'s watch, or inflight/'s: the instance watches nothing else.
	if wd == w.wds[0] {
		//: the queued messages.
		return w.states[0]
	}
	//: the leases.
	return w.states[1]
}

// close releases the instance, and every watch with it.
func (w *stateWatch) close() error {
	//: the descriptor, once.
	return w.file.Close()
}

// readEntries reads dir whole into index through getdents64, from buf: a name
// already indexed is looked up in place and costs no allocation, and only a
// name the index lacks is copied out.
func readEntries(dir *os.File, buf []byte, index *dirIndex) error {
	held := int(dir.Fd())
	defer runtime.KeepAlive(dir)
	//: until the end of the directory.
	for {
		filled, readErr := syscall.ReadDirent(held, buf)
		//: a signal: read again.
		if readErr == syscall.EINTR {
			continue
		}
		//: the medium refused.
		if readErr != nil {
			//: QueueBackendFailed, by the caller.
			return readErr
		}
		//: the end of the directory.
		if filled <= 0 {
			//: read whole.
			return nil
		}
		seeDirents(buf[:filled], index)
	}
}

// seeDirents hands every name of one getdents64 read to index.
func seeDirents(entries []byte, index *dirIndex) {
	//: one record after another; a truncated one is no record.
	for len(entries) > direntNameAt {
		size := int(binary.NativeEndian.Uint16(entries[direntReclen:]))
		//: a record the kernel never writes ends the read.
		if size <= direntNameAt || size > len(entries) {
			//: what came before is read.
			return
		}
		name := entries[direntNameAt:size]
		entries = entries[size:]
		//: the name ends at its NUL.
		if cut := bytes.IndexByte(name, 0); cut >= 0 {
			name = name[:cut]
		}
		index.seeBytes(name)
	}
}
