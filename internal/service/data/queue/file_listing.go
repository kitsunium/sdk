package queue

import (
	"cmp"
	"slices"
	"sync/atomic"
	"time"
)

// racyMargin is how much older than the listing that read it a directory's
// modification instant must be for the listing to be reused (issue #811).
//
// A directory's mtime is the filesystem's record of its last change, stamped
// from the kernel's coarse clock — up to one tick behind the real one — and
// truncated to the filesystem's granularity: a nanosecond on ext4, XFS, Btrfs
// or APFS, one second on ext3 or HFS+, two on FAT. A change made after a
// listing began can therefore carry the very mtime the listing recorded, when
// both fall in one tick: the directory changed, and its mtime did not. Git
// meets the same fact in its index and names it "racily clean" — an entry
// whose file changed in the second the index was written is looked at again,
// whatever its stat says. The broker does the same: a listing whose
// directory's mtime is within racyMargin of the instant the listing began is
// never reused, so a publication in the same tick as a listing is seen by the
// next Receive. Three seconds covers the coarsest granularity named above
// plus the coarse clock's lag, with room to spare.
const racyMargin time.Duration = 3 * time.Second

// listingMaxAge bounds how long a listing is reused even when its directory's
// mtime says nothing changed: past it, the next look reads the directory
// again. It is what keeps "finally seen" unconditional where the mtime cannot
// be trusted at all — a wall clock stepped back across a listing, a network
// filesystem stamping with a server clock far behind this one, a tool that
// restores a directory's mtime — at the cost of one read per directory every
// listingMaxAge on a queue nobody touches. It runs on the monotonic clock, so
// a step of the wall clock does not stretch it.
const listingMaxAge time.Duration = 10 * time.Second

// stateListing is one state directory's names as one read found them, and the
// two instants that decide whether the next look may reuse them.
type stateListing struct {
	// listedAt is the real clock's reading BEFORE the directory was read:
	// every change the read could have missed happened after it.
	listedAt time.Time
	// modTime is the directory's mtime as stat read it before the listing.
	modTime time.Time
	// names is the directory's entries in the state's order. It is never
	// written once the listing is stored: every caller only reads it.
	names []string
}

// reusable reports whether this listing still names what the directory
// holds: the directory's mtime is the one the listing recorded, that mtime
// is older than the listing by racyMargin — a change in the listing's own
// tick could not have left it unchanged —, and the listing is younger than
// listingMaxAge.
func (l *stateListing) reusable(modTime, now time.Time) bool {
	//: three conditions, each necessary.
	return modTime.Equal(l.modTime) &&
		modTime.Before(l.listedAt.Add(-racyMargin)) &&
		now.Sub(l.listedAt) < listingMaxAge
}

// listings holds the last listing of each state a Receive, a listing or a
// count reads. The reservations' directory is not among them: its sweep reads
// it once a minute at most.
type listings struct {
	// wall reads the real clock — never the broker's, which a test may hold
	// still: the instants compared are the filesystem's, and it stamps them
	// from the real clock. A field so a test can move it.
	wall     func() time.Time
	ready    atomic.Pointer[stateListing]
	inflight atomic.Pointer[stateListing]
	dead     atomic.Pointer[stateListing]
}

// slot returns the cell holding state's last listing, nil for a directory
// that is never cached.
func (l *listings) slot(state string) *atomic.Pointer[stateListing] {
	//: the three states of a message.
	switch state {
	case dirReady:
		return &l.ready
	case dirInflight:
		return &l.inflight
	case dirDead:
		return &l.dead
	default:
		//: the reservations.
		return nil
	}
}

// namesOf lists one state directory in the state's order (sortState) —
// which, because of the zero-padded instant that opens every name after its
// level, is the order of TIME within a level.
//
// A state's listing is reused while its directory's mtime has not moved and
// was already old when the listing began (racyMargin, listingMaxAge): an
// idle consumer's poll, a count, a listing of a queue nobody touches cost one
// stat per directory rather than a read and a sort of every entry (issue
// #811). Any change moves the mtime — a publication, a lease, an
// acknowledgement, in this process or another —, and the next look reads the
// directory again, so a busy queue is read on every Receive as before: that
// is O(entries) per call, measured in BENCH.md, and a caller with millions in
// flight wants a broker over a system built for that, behind this same port.
func (b *fileBroker) namesOf(state string) (names []string, err error) {
	slot := b.listed.slot(state)
	//: a directory that is never cached is read every time.
	if slot == nil {
		//: QueueBackendFailed when the medium refuses.
		return b.readNames(state)
	}
	//: BEFORE the stat and the read: a change the read misses is later still.
	listedAt := b.listed.wall()
	info, statErr := b.root.Stat(state)
	//: a state directory that vanished under a live broker is a real fault.
	if statErr != nil {
		//: QueueBackendFailed.
		return nil, backendFailed("stat", state, statErr)
	}
	//: nothing changed since a listing old enough to trust.
	if cached := slot.Load(); cached != nil && cached.reusable(info.ModTime(), listedAt) {
		//: the names as they were, and still are.
		return cached.names, nil
	}
	read, readErr := b.readNames(state)
	//: the medium refused.
	if readErr != nil {
		//: QueueBackendFailed.
		return nil, readErr
	}
	//: the mtime is the stat's, taken BEFORE the read: a change between the two
	//: is in the names and moves the mtime, so it only costs one read more.
	slot.Store(&stateListing{listedAt: listedAt, modTime: info.ModTime(), names: read})
	//: what the directory holds.
	return read, nil
}

// readNames reads every entry name of one directory, sorted. Names alone:
// the grammar needs nothing else, and a fs.DirEntry per entry was half of
// what a listing allocated.
func (b *fileBroker) readNames(state string) (names []string, err error) {
	dir, openErr := b.root.Open(state)
	//: a state directory that vanished under a live broker is a real fault.
	if openErr != nil {
		//: QueueBackendFailed.
		return nil, backendFailed("readdir", state, openErr)
	}
	read, readErr := dir.Readdirnames(-1)
	closeErr := dir.Close()
	//: the read refused, or the descriptor would not close.
	if readErr != nil || closeErr != nil {
		//: QueueBackendFailed, the read's cause first.
		return nil, backendFailed("readdir", state, cmp.Or(readErr, closeErr))
	}
	sortState(state, read)
	//: in the state's order, which is the order of time.
	return read, nil
}

// sortState sorts one state directory's names in the order its readers walk
// them: queued names by level, then visibility (compareReady); in-flight and
// dead names by what follows their level (compareStem) — a deadline, an
// enqueue instant; any other directory by name.
func sortState(state string, names []string) {
	//: the state's order.
	switch state {
	case dirReady:
		slices.SortFunc(names, compareReady)
	case dirInflight, dirDead:
		slices.SortFunc(names, compareStem)
	default:
		slices.Sort(names)
	}
}
