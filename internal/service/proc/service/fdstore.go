package service

import (
	"os"
	"slices"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/systemd/listen"
)

// The descriptor store (S0d, kitsunium/sdk#736) is service.c's fd store: what
// a service hands with FDSTORE=1 is kept and given back to its next main
// process. The reducer decides — what is kept, refused, removed, handed,
// released — over plain entries; the shell holds the descriptors by id.

const (
	// defaultFDName names a descriptor stored without a valid FDNAME=, as
	// sd_notify(3) names it.
	defaultFDName string = "stored"
	// dupEarlier marks a descriptor that repeats one before it in its message.
	dupEarlier uint64 = ^uint64(0)
)

// notifyStore takes an admitted message's descriptors, after its states, its
// metadata and its watchdog (service.c's order): FDSTORE=1 keeps them, every
// one not kept is closed, then FDSTOREREMOVE=1 removes a name.
func (st *step) notifyStore(f map[string]string) {
	files := st.ev.msg.Files
	keep := make([]bool, len(files))
	//: FDSTORE=1.
	if f["FDSTORE"] == "1" {
		st.storeFiles(f, keep)
	}
	st.closeUnkept(files, keep)
	//: FDSTOREREMOVE=1, with the FDNAME= it removes.
	if f["FDSTOREREMOVE"] == "1" {
		st.removeNamed(f["FDNAME"])
	}
}

// storeFiles keeps FDSTORE=1's descriptors, under FDNAME= and FDPOLL=: a
// duplicate of a stored object is dropped, one past FileDescriptorStoreMax=
// refused and counted — a unit without a store refuses every one. With
// MAINPIDFD=1 the first descriptor is the pidfd, never stored.
func (st *step) storeFiles(f map[string]string, keep []bool) {
	m := st.m
	files := st.ev.msg.Files
	first := 0
	//: the pidfd MAINPID= was read from.
	if f["MAINPIDFD"] == "1" {
		first = 1
	}
	//: nothing to store.
	if first >= len(files) {
		return
	}
	st.ownStore()
	name, poll := fdName(f["FDNAME"]), f["FDPOLL"] != "0"
	var kept []*os.File
	var entries []storeEntry
	//: each descriptor in turn.
	for i := first; i < len(files); i++ {
		//: an object stored already.
		if st.duplicate(i) {
			continue
		}
		//: the store is full, or there is none.
		if len(m.store) >= m.s.storeMax {
			m.storeRefused++
			continue
		}
		m.storeSeq++
		e := storeEntry{name: name, id: m.storeSeq, poll: poll}
		m.store = append(slices.Clip(m.store), e)
		m.storeKey = m.s.storeKey
		keep[i] = true
		kept, entries = append(kept, files[i]), append(entries, e)
	}
	//: the shell keeps them.
	if len(kept) > 0 {
		st.emit(&effect{kind: efStore, files: kept, entries: entries})
	}
}

// duplicate reports whether a message's i-th descriptor refers to an object
// the store holds, or repeats one before it, as the shell found it.
func (st *step) duplicate(i int) bool {
	dups := st.ev.dupOf
	//: nothing found for it.
	if i >= len(dups) || dups[i] == 0 {
		//: no.
		return false
	}
	//: an earlier one of the message, or a stored one still held.
	return dups[i] == dupEarlier || slices.ContainsFunc(st.m.store, func(e storeEntry) bool { return e.id == dups[i] })
}

// closeUnkept closes every descriptor of a message the store did not keep.
func (st *step) closeUnkept(files []*os.File, keep []bool) {
	var drop []*os.File
	//: each descriptor.
	for i, f := range files {
		//: not kept.
		if !keep[i] {
			drop = append(drop, f)
		}
	}
	st.closeFiles(drop)
}

// closeFiles asks the shell to close descriptors nobody keeps.
func (st *step) closeFiles(files []*os.File) {
	//: something to close.
	if len(files) > 0 {
		st.emit(&effect{kind: efClose, files: files})
	}
}

// removeNamed is FDSTOREREMOVE=1: every descriptor stored under the name is
// closed. Without a valid FDNAME= nothing is, as systemd ignores the request.
func (st *step) removeNamed(name string) {
	//: no name, or one no descriptor can bear.
	if name == "" || listen.CheckName(name) != nil {
		return
	}
	st.dropWhere(func(e storeEntry) bool { return e.name == name })
}

// onHangup takes a stored descriptor's hangup or error: it leaves the store.
func (st *step) onHangup() {
	id := st.ev.stored
	st.dropWhere(func(e storeEntry) bool { return e.id == id })
}

// dropWhere closes and forgets every stored descriptor drop selects.
func (st *step) dropWhere(drop func(storeEntry) bool) {
	m := st.m
	var gone, kept []storeEntry
	//: each entry, one way or the other.
	for _, e := range m.store {
		//: selected.
		if drop(e) {
			gone = append(gone, e)
			continue
		}
		kept = append(kept, e)
	}
	//: nothing selected.
	if len(gone) == 0 {
		return
	}
	m.store = kept
	//: an empty store belongs to nobody.
	if len(kept) == 0 {
		m.storeKey = ""
	}
	st.emit(&effect{kind: efStoreDrop, entries: gone})
}

// releaseStore closes the whole store.
func (st *step) releaseStore() {
	m := st.m
	//: nothing held.
	if len(m.store) == 0 {
		return
	}
	st.emit(&effect{kind: efStoreDrop, entries: m.store})
	m.store, m.storeKey = nil, ""
}

// ownStore closes a store filled for a unit this one no longer is — its key
// another's, as a store restored from elsewhere may be (S0h) — counting
// every descriptor so discarded.
func (st *step) ownStore() {
	m := st.m
	//: empty, or the unit's own.
	if len(m.store) == 0 || m.storeKey == m.s.storeKey {
		return
	}
	m.storeDiscarded += uint64(len(m.store))
	st.releaseStore()
}

// handOver is what a main command is handed: the unit's own store, newest
// first, as service_collect_fds walks systemd's list.
func (st *step) handOver() []storeEntry {
	st.ownStore()
	store := st.m.store
	//: nothing to hand.
	if len(store) == 0 {
		//: none.
		return nil
	}
	out := slices.Clone(store)
	slices.Reverse(out)
	//: newest first.
	return out
}

// settleStore releases the store when FileDescriptorStorePreserve= says the
// unit is done with it: restart once it rests dead with no job and no
// restart due, on-success once it is failed for good. no is released at every
// end of a run (enterDead), yes only by Close.
func (st *step) settleStore() {
	m := st.m
	//: nothing held, or a job still to run.
	if len(m.store) == 0 || m.job.kind != jobNone || m.pending.kind != jobNone {
		return
	}
	release := false
	//: by the policy.
	switch m.s.preserve {
	//: dead, failed or skipped.
	case coresvc.PreserveRestart:
		release = isDead(m.state)
	//: failed for good.
	case coresvc.PreserveOnSuccess:
		release = m.state == coresvc.StateFailed
	//: no and yes release elsewhere.
	default:
	}
	//: released.
	if release {
		st.releaseStore()
	}
}

// fdName is FDNAME='s name, or "stored" when it is absent or no descriptor
// may bear it: systemd ignores an invalid name.
func fdName(v string) string {
	//: absent, or invalid.
	if v == "" || listen.CheckName(v) != nil {
		//: the default.
		return defaultFDName
	}
	//: the service's.
	return v
}
