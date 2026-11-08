package service

import (
	"os"
	"slices"
	"strconv"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/systemd/listen"
)

// held is one descriptor the shell keeps for the store: the descriptor, what
// Unit.FDStore lists of it, and whether a hangup watch holds it.
type held struct {
	file    *os.File
	view    coresvc.StoredFDValue
	watched bool
}

// keep holds efStore's descriptors under their ids, watches each one asked
// for a hangup — an object epoll cannot watch stays, unwatched —, and
// publishes the listing.
func (a *actor) keep(ef *effect) {
	//: each descriptor with its entry.
	for i, f := range ef.files {
		e := ef.entries[i]
		info, err := f.Stat()
		//: an object fstat cannot read is listed by its name alone.
		if err != nil {
			info = nil
		}
		h := &held{file: f, view: describe(e.name, info)}
		//: FDPOLL= not 0.
		if e.poll {
			h.watched = a.watchHangup(f, e.id)
		}
		h.view.Poll = h.watched
		a.store[e.id] = h
		a.storeOrder = append(a.storeOrder, e.id)
	}
	a.publishStore()
}

// drop closes efStoreDrop's descriptors — out of the hangup watch first, so
// no event names a descriptor reused — and forgets them.
func (a *actor) drop(entries []storeEntry) {
	//: each entry the shell holds.
	for _, e := range entries {
		h := a.store[e.id]
		//: held no longer.
		if h == nil {
			continue
		}
		//: out of the watch before the close.
		if h.watched {
			a.hangups.remove(h.file)
		}
		gone(h.file.Close())
		delete(a.store, e.id)
	}
	a.storeOrder = slices.DeleteFunc(a.storeOrder, func(id uint64) bool { return a.store[id] == nil })
	a.publishStore()
}

// publishStore publishes Unit.FDStore's listing: newest first, as the next
// main process receives it.
func (a *actor) publishStore() {
	view := make([]coresvc.StoredFDValue, 0, len(a.storeOrder))
	//: from the newest.
	for _, id := range slices.Backward(a.storeOrder) {
		view = append(view, a.store[id].view)
	}
	a.u.mu.Lock()
	a.u.stored = view
	a.u.mu.Unlock()
}

// watchHangup watches f for a hangup, starting the watch on its first use;
// it reports whether f is watched.
//
// Goroutine lifecycle: the watch's one goroutine (hangupWatch.start) posts
// each hangup to the actor until closeStore stops it, and is joined there.
func (a *actor) watchHangup(f *os.File, id uint64) bool {
	//: the first descriptor to watch.
	if a.hangups == nil {
		w, err := newHangupWatch()
		//: no watch on this kernel: nothing is.
		if err != nil {
			//: unwatched.
			return false
		}
		a.hangups = w
		u := a.u
		w.start(func(stored uint64) bool { return u.post(&event{kind: evHangup, stored: stored}) })
	}
	//: watched, or an object epoll cannot watch.
	return a.hangups.add(f, id)
}

// closeStore ends the store with the actor: the watch stopped and joined,
// every descriptor closed — unless the hand-over (S0h) takes them first.
func (a *actor) closeStore() {
	//: a watch runs.
	if a.hangups != nil {
		a.hangups.stop()
	}
	//: each descriptor held.
	for _, h := range a.store {
		gone(h.file.Close())
	}
	clear(a.store)
	a.storeOrder = nil
	a.publishStore()
}

// dupsOf says, for each descriptor of a FDSTORE=1 message, which stored one
// it refers to the same object as — or that one before it in the message
// does (dupEarlier) —, as systemd's same_fd decides it.
func (a *actor) dupsOf(files []*os.File) []uint64 {
	out := make([]uint64, len(files))
	//: each descriptor.
	for i, f := range files {
		//: an earlier one of the message.
		if slices.ContainsFunc(files[:i], func(g *os.File) bool { return sameFile(f, g) }) {
			out[i] = dupEarlier
			continue
		}
		//: a stored one.
		for id, h := range a.store {
			//: the same object.
			if sameFile(f, h.file) {
				out[i] = id
				break
			}
		}
	}
	//: the findings.
	return out
}

// describe is what Unit.FDStore lists of a stored descriptor: its name and
// what fstat read of it, when it could.
func describe(name string, info os.FileInfo) coresvc.StoredFDValue {
	v := coresvc.StoredFDValue{Name: name}
	//: an object fstat cannot read: its name alone.
	if info == nil {
		//: the name.
		return v
	}
	v.Mode = info.Mode()
	v.Device, v.Inode = fileID(info)
	//: the description.
	return v
}

// handOver gives a main command what it is owed, at fd 3 on after the
// sockets the template's spec already activates: owed, in order — the
// store's newest first (S0d), a connection (S0f) —, each with its name,
// LISTEN_FDS counting them all and LISTEN_PID the child's own
// (listen.PrepareFiles). The descriptors are passed as they are; nothing owed
// leaves spec as it was.
func (a *actor) handOver(spec *coreproc.Spec, owed []listen.NamedFileValue) error {
	//: nothing owed.
	if len(owed) == 0 {
		//: the spec as built.
		return nil
	}
	named := activation(spec)
	spec.ExtraFiles = slices.Clone(spec.ExtraFiles[len(named):])
	//: the sockets, then what is owed.
	return listen.PrepareFiles(spec, append(named, owed...))
}

// stored is the store's part of a main command's hand-over, as the reducer
// listed it.
func (a *actor) stored(entries []storeEntry) []listen.NamedFileValue {
	out := make([]listen.NamedFileValue, 0, len(entries))
	//: each entry the shell holds.
	for _, e := range entries {
		//: held.
		if h := a.store[e.id]; h != nil {
			out = append(out, listen.NamedFileValue{Name: e.name, File: h.file})
		}
	}
	//: in the reducer's order.
	return out
}

// activation reads the sockets a spec already activates — its first
// LISTEN_FDS ExtraFiles, named by LISTEN_FDNAMES (listen.Prepare's) — so a
// hand-over follows them and LISTEN_FDS counts every one; a malformed count
// is no activation.
func activation(spec *coreproc.Spec) []listen.NamedFileValue {
	values := envMap(spec.Env)
	n, err := strconv.Atoi(values["LISTEN_FDS"])
	//: none, or no count the spec can hold.
	if err != nil || n <= 0 || n > len(spec.ExtraFiles) {
		//: none.
		return nil
	}
	names := strings.Split(values["LISTEN_FDNAMES"], ":")
	//: names that do not pair with the sockets.
	if len(names) != n {
		names = make([]string, n)
	}
	out := make([]listen.NamedFileValue, 0, n)
	//: each socket with its name.
	for i := range n {
		out = append(out, listen.NamedFileValue{Name: names[i], File: spec.ExtraFiles[i]})
	}
	//: the activation.
	return out
}
