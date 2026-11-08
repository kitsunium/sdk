package cgroup

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// The files and the inotify(7) record a watch reads.
const (
	// eventsFile is the group's populated and frozen keys (cgroup.events).
	eventsFile string = "cgroup.events"
	// eventsBuffer holds many records: a burst is read in few reads, and a
	// queue overflow is harmless because every read is followed by a fresh
	// read of the files.
	eventsBuffer int = 4096
	// noWatch is the descriptor of a watch that was not added or is gone.
	noWatch int32 = -1
	// recordWd, recordMask and recordLen are the offsets of struct
	// inotify_event's wd, mask and len.
	recordWd   int = 0
	recordMask int = 4
	recordLen  int = 12
	// goneMask is what the parent's watch hears of the group's removal or
	// rename: cgroupfs removes a group's files without an IN_IGNORED for a
	// watch on them, and only the parent's IN_DELETE says the group went
	// (measured on Linux 6.12).
	goneMask uint32 = syscall.IN_DELETE | syscall.IN_MOVED_FROM
)

// watch follows one group's cgroup.events and memory.events, and its
// parent's directory for the group's removal, through one inotify
// descriptor.
type watch struct {
	// notes is the inotify descriptor, non-blocking and on the runtime's
	// poller so a deadline ends a read.
	notes *os.File
	// dir is the group's directory, and name its last element as the
	// parent's records spell it.
	dir  string
	name []byte
	// events, memory and parent are the watch descriptors of cgroup.events,
	// of memory.events and of the parent directory, noWatch for one absent or
	// gone.
	events int32
	memory int32
	parent int32
}

// Watch returns a channel holding the group's state first, then its state
// after each change: populated, frozen, the memory events. The channel
// holds one value, and a burst coalesces into the latest: the watch never
// blocks on its reader. It is closed when ctx ends, and when the group is
// gone, after a last value holding no process; a reader whose ctx is not
// done reads the close as the group's removal. CgroupReadFailed when the
// group's cgroup.events cannot be watched or read.
func (g *controlGroup) Watch(ctx context.Context) (<-chan coreproc.GroupEventsValue, error) {
	//: the group's own directory.
	return watchDir(ctx, g.dir)
}

// watchDir installs the watches on dir's files, reads the first state after
// they are in place — so no change between the two is missed — and starts
// the goroutine that publishes every later one.
//
// Goroutine lifecycle: one goroutine per call, reading the inotify
// descriptor; it ends when ctx ends (a past read deadline ends the read), when
// the group is removed, or when the descriptor fails, and closes the channel
// and the descriptor as it goes.
func watchDir(ctx context.Context, dir string) (<-chan coreproc.GroupEventsValue, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	//: no inotify instance left to this process.
	if err != nil {
		//: CGROUP_READ_FAILED, with the directory.
		return nil, cgroupReadFailed(os.NewSyscallError("inotify_init1", err), dir)
	}
	//: non-blocking, so the os package puts it on the poller.
	w := &watch{notes: os.NewFile(uintptr(fd), "inotify"), dir: dir, name: []byte(filepath.Base(dir)), events: noWatch, memory: noWatch, parent: noWatch}
	events, err := syscall.InotifyAddWatch(fd, filepath.Join(dir, eventsFile), syscall.IN_MODIFY)
	//: no cgroup.events: the group is gone, or the root names no group.
	if err != nil {
		w.close()
		//: CGROUP_READ_FAILED, with the directory.
		return nil, cgroupReadFailed(os.NewSyscallError("inotify_add_watch", err), dir)
	}
	w.events = int32(events)
	//: memory.events only where the memory controller is enabled.
	if memory, merr := syscall.InotifyAddWatch(fd, filepath.Join(dir, memoryEventsFile), syscall.IN_MODIFY); merr == nil {
		w.memory = int32(memory)
	}
	//: the parent hears the group's rmdir; without it, ctx alone ends the watch.
	if parent, perr := syscall.InotifyAddWatch(fd, filepath.Dir(dir), goneMask|syscall.IN_ONLYDIR); perr == nil {
		w.parent = int32(parent)
	}
	first, err := w.state()
	//: the files went between the watch and the read.
	if err != nil {
		w.close()
		//: CGROUP_READ_FAILED.
		return nil, err
	}
	out := make(chan coreproc.GroupEventsValue, 1)
	out <- first
	go w.run(ctx, out, first)
	//: the first state is already in the channel.
	return out, nil
}

// run reads inotify records until ctx ends, the group goes or the
// descriptor fails, publishing the files' state after each read that changed
// it from last or followed a change the kernel announced.
func (w *watch) run(ctx context.Context, out chan coreproc.GroupEventsValue, last coreproc.GroupEventsValue) {
	defer close(out)
	defer w.close()
	//: a deadline in the past ends the read in flight, and every later one.
	stop := context.AfterFunc(ctx, func() { w.expire() })
	defer stop()
	buf := make([]byte, eventsBuffer)
	//: one read per batch of records.
	for {
		n, err := w.notes.Read(buf)
		//: ctx ended (the deadline), or the descriptor failed.
		if err != nil || ctx.Err() != nil {
			return
		}
		removed, announced := w.scan(buf[:n])
		state, serr := w.state()
		//: the group is gone: a last value holds no process, and the watch ends.
		if removed || serr != nil {
			publish(out, coreproc.GroupEventsValue{})
			return
		}
		//: a change, or a change the kernel announced that a burst undid
		//: before this read — populated 0, 1, 0 between two reads: a reader
		//: that read the group meanwhile must learn it changed back. A
		//: sibling's removal is no change and publishes nothing.
		if state != last || announced {
			publish(out, state)
			last = state
		}
	}
}

// expire sets a read deadline in the past, which ends the read in flight.
func (w *watch) expire() {
	//: a descriptor already closed has no read to end.
	if err := w.notes.SetReadDeadline(time.Unix(1, 0)); err != nil {
		return
	}
}

// close releases the inotify descriptor and, with it, every watch.
func (w *watch) close() {
	//: nothing is left to release when the close fails.
	if err := w.notes.Close(); err != nil {
		return
	}
}

// scan walks the records of one read and reports whether the group is gone
// — its parent's IN_DELETE or IN_MOVED_FROM naming it, or cgroup.events'
// watch removed (IN_IGNORED) — and whether the kernel announced a change of
// cgroup.events or memory.events (IN_MODIFY). memory.events' watch going
// alone — the controller disabled — only forgets it.
func (w *watch) scan(records []byte) (removed, announced bool) {
	//: one struct inotify_event and its name per record.
	for off := 0; off+syscall.SizeofInotifyEvent <= len(records); {
		wd := int32(binary.NativeEndian.Uint32(records[off+recordWd:]))
		mask := binary.NativeEndian.Uint32(records[off+recordMask:])
		start := off + syscall.SizeofInotifyEvent
		off = min(start+int(binary.NativeEndian.Uint32(records[off+recordLen:])), len(records))
		removed = w.gone(wd, mask, records[start:off]) || removed
		announced = announced || w.modified(wd, mask)
	}
	//: whether the group is gone, and whether its files changed.
	return removed, announced
}

// modified reports a record announcing a change of the group's
// cgroup.events or memory.events.
func (w *watch) modified(wd int32, mask uint32) bool {
	//: IN_MODIFY on one of the group's own files.
	return mask&syscall.IN_MODIFY != 0 && wd != noWatch && (wd == w.events || wd == w.memory)
}

// gone applies one record: it reports whether the record says the group
// went, and forgets memory.events' watch when that file alone went.
func (w *watch) gone(wd int32, mask uint32, name []byte) bool {
	//: the parent names the entry it lost, NUL-padded.
	if wd == w.parent && mask&goneMask != 0 {
		return bytes.Equal(bytes.TrimRight(name, "\x00"), w.name)
	}
	//: only a watch's removal changes what is watched.
	if mask&syscall.IN_IGNORED == 0 {
		return false
	}
	//: the memory controller's file alone.
	if wd == w.memory {
		w.memory = noWatch
	}
	//: the group's own file: the group is gone.
	return wd == w.events
}

// state reads cgroup.events, then memory.events where it is watched.
func (w *watch) state() (coreproc.GroupEventsValue, error) {
	var state coreproc.GroupEventsValue
	content, err := os.ReadFile(filepath.Join(w.dir, eventsFile))
	//: the group's own file is gone.
	if err != nil {
		//: CGROUP_READ_FAILED, with the directory.
		return state, cgroupReadFailed(err, w.dir)
	}
	flatKeyed(string(content), func(key string, value int64) { eventKey(&state, key, value) })
	//: the memory counters, where the controller gives them.
	if w.memory != noWatch {
		//: a file gone between two reads leaves the counters at zero.
		if memory, merr := os.ReadFile(filepath.Join(w.dir, memoryEventsFile)); merr == nil {
			flatKeyed(string(memory), func(key string, value int64) { memoryEvent(&state.MemoryEvents, key, value) })
		}
	}
	//: the state now.
	return state, nil
}

// eventKey applies one cgroup.events key.
func eventKey(state *coreproc.GroupEventsValue, key string, value int64) {
	switch strings.TrimSpace(key) {
	//: a live process in the group or beneath it.
	case "populated":
		state.Populated = value != 0
	//: the group is frozen.
	case "frozen":
		state.Frozen = value != 0
	//: a key the SDK does not read.
	default:
	}
}

// publish puts v in out, replacing a value the reader has not taken: the
// watch never blocks, and the reader always gets the latest state.
func publish(out chan coreproc.GroupEventsValue, v coreproc.GroupEventsValue) {
	//: the channel holds one value: it is either empty (the send is ready) or
	//: full (the receive is), and this goroutine alone sends and closes it.
	for {
		select {
		//: room for the value.
		case out <- v:
			return
		//: full: the stale value goes, and the send is tried again.
		case _, open := <-out:
			//: never closed while this goroutine publishes.
			if !open {
				return
			}
		}
	}
}
