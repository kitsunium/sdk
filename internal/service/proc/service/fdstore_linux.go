//go:build linux

package service

import (
	"os"
	"sync"
	"syscall"
)

// hangupWatch is the store's hangup watch: an epoll set every pollable
// stored descriptor is in with no event asked — EPOLLHUP and EPOLLERR are
// always reported, as systemd's sd_event_add_io with events 0 —, each once
// (EPOLLONESHOT) under its id, and a pipe that ends the wait.
type hangupWatch struct {
	running sync.WaitGroup
	wake    [2]int
	epfd    int
}

// newHangupWatch opens the epoll set and its wake pipe, id 0.
func newHangupWatch() (*hangupWatch, error) {
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	//: no epoll.
	if err != nil {
		//: the kernel's refusal.
		return nil, err
	}
	w := &hangupWatch{epfd: epfd}
	//: the pipe that ends the wait.
	if err = syscall.Pipe2(w.wake[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		gone(syscall.Close(epfd))
		//: the kernel's refusal.
		return nil, err
	}
	//: the wake pipe in the set, under id 0.
	if err = syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, w.wake[0], new(syscall.EpollEvent{Events: syscall.EPOLLIN})); err != nil {
		w.closeFDs()
		//: the kernel's refusal.
		return nil, err
	}
	//: the watch.
	return w, nil
}

// add watches f under id; false for an object epoll cannot watch (EPERM: a
// regular file, a directory).
func (w *hangupWatch) add(f *os.File, id uint64) bool {
	ev := &syscall.EpollEvent{Events: syscall.EPOLLONESHOT, Fd: int32(uint32(id)), Pad: int32(uint32(id >> 32))}
	added := false
	control(f, func(fd int) { added = syscall.EpollCtl(w.epfd, syscall.EPOLL_CTL_ADD, fd, ev) == nil })
	//: watched or not.
	return added
}

// remove takes f out of the set, before it is closed.
func (w *hangupWatch) remove(f *os.File) {
	control(f, func(fd int) { gone(syscall.EpollCtl(w.epfd, syscall.EPOLL_CTL_DEL, fd, &syscall.EpollEvent{})) })
}

// start runs the watch.
//
// Goroutine lifecycle: one, posting each hangup's id until the wake pipe or a
// post the unit no longer takes ends it; stop joins it.
func (w *hangupWatch) start(post func(uint64) bool) {
	w.running.Add(1)
	//: Goroutine lifecycle: ended by stop's wake, or a unit gone; joined by
	//: stop.
	go func() {
		defer w.running.Done()
		w.run(post)
	}()
}

// run posts each hangup's id until the wake pipe, or a post the unit no
// longer takes, ends it.
func (w *hangupWatch) run(post func(uint64) bool) {
	events := make([]syscall.EpollEvent, 8)
	//: until stopped.
	for {
		n, err := syscall.EpollWait(w.epfd, events, -1)
		//: a signal interrupted the wait.
		if err == syscall.EINTR {
			continue
		}
		//: the set is unusable.
		if err != nil {
			return
		}
		//: each event.
		for i := range n {
			id := uint64(uint32(events[i].Fd)) | uint64(uint32(events[i].Pad))<<32
			//: the wake pipe, or a unit gone.
			if id == 0 || !post(id) {
				return
			}
		}
	}
}

// stop ends run, waits for it, then closes the set and the pipe: no wait is
// left on a descriptor number that could be reused.
func (w *hangupWatch) stop() {
	_, err := syscall.Write(w.wake[1], []byte{1})
	gone(err)
	w.running.Wait()
	w.closeFDs()
}

// closeFDs closes the set and the pipe.
func (w *hangupWatch) closeFDs() {
	gone(syscall.Close(w.epfd))
	gone(syscall.Close(w.wake[0]))
	gone(syscall.Close(w.wake[1]))
}

// control lends f's descriptor for one call without the blocking mode
// os.File.Fd would force on it — a mode the service shares, since a stored
// descriptor is the service's own open file.
func control(f *os.File, fn func(fd int)) {
	rc, err := f.SyscallConn()
	//: a closed file.
	if err != nil {
		return
	}
	gone(rc.Control(func(fd uintptr) { fn(int(fd)) }))
}

// sameFile is systemd's same_fd without kcmp, which the standard library
// does not name everywhere: the same inode on the same device, never a
// device node — two of them may be two contexts —, opened in the same access
// mode, which tells a pipe's two ends apart.
func sameFile(a, b *os.File) bool {
	ia, errA := a.Stat()
	ib, errB := b.Stat()
	//: unreadable, or another object.
	if errA != nil || errB != nil || !os.SameFile(ia, ib) || ia.Mode()&os.ModeDevice != 0 {
		//: not the same.
		return false
	}
	//: the same access mode.
	return accessMode(a) == accessMode(b)
}

// accessMode is f's O_ACCMODE, -1 when it cannot be read.
func accessMode(f *os.File) int {
	mode := -1
	control(f, func(fd int) {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		//: read.
		if errno == 0 {
			mode = int(flags) & syscall.O_ACCMODE
		}
	})
	//: the mode.
	return mode
}

// fileID is what fstat names an object by: its device and inode.
func fileID(info os.FileInfo) (device, inode uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	//: no stat.
	if !ok {
		//: unknown.
		return 0, 0
	}
	//: the pair.
	return uint64(st.Dev), st.Ino
}
