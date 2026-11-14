package queue

import (
	"bytes"
	"cmp"
	"os"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// watchedRelistAge is how long the index of a watched state directory goes
// without a full read (issue #811). The kernel names every change of a local
// directory, so the read is a net under a defect of this code, never what
// keeps a publication visible: a minute keeps it out of a busy Receive's way
// at any depth.
const watchedRelistAge time.Duration = time.Minute

// readyLevels is how many levels the queued names sort into: one heap each,
// walked highest first, as the digit that opens a leveled name sorts.
const readyLevels int = int(corequeue.PriorityHighest-corequeue.PriorityLowest) + 1

// direntBufferBytes is the buffer one read of a state directory fills —
// os.ReadDir's own block size. It is allocated once a broker.
const direntBufferBytes int = 8 << 10

// indexItem is one name in its heap: its instant, read once, ahead of the
// name it was read from.
type indexItem struct {
	// name is the entry's name in its directory.
	name string
	// at is the instant the name sorts on: a visibility, a lease's deadline.
	at int64
}

// compareItems orders one heap's names as the directory's sorted listing did:
// by their instant, then by the rest of the name — whatever their level,
// since a level has a heap of its own (compareStem).
func compareItems(first, second indexItem) int {
	//: the instant leads every stem, at a fixed width, so it decides first.
	return cmp.Or(cmp.Compare(first.at, second.at), compareStem(first.name, second.name))
}

// indexSlot is where one indexed name sits.
type indexSlot struct {
	// name is the map's key again, so a mark rewrites the entry without
	// building the key anew.
	name string
	// handle names the item in its heap.
	handle heap.Handle
	// seen is the generation of the last full read that met the name.
	seen uint32
	// bucket is the heap the item is in: its level's for ready/, the one heap
	// for inflight/.
	bucket uint8
}

// dirIndex is the broker's knowledge of one state directory's messages: every
// name its grammar reads, in a heap per level, and a map from name to place.
// It is a hint and never the truth: the rename that takes a message is still
// the arbiter, a name in excess costs one ENOENT, and a name missing is read
// at the next full read (file_index.go's fileIndex).
type dirIndex struct {
	// slots maps every indexed name to its heap and handle.
	slots map[string]indexSlot
	// parse is the state's grammar: parseReady or parseInflight.
	parse func(string) (nameValue, bool)
	// relistedAt is when the directory was last read whole, on the listings'
	// real clock.
	relistedAt time.Time
	// stamp is the last full read's, for the rule that decides, where no
	// kernel names the changes, whether the directory must be read again;
	// zero before the first.
	stamp listingStamp
	// state is the directory: dirReady or dirInflight.
	state string
	// heaps holds one heap per level for ready/, walked level by level, and
	// one for inflight/.
	heaps []*heap.IndexedHeap[indexItem]
	// gen counts the full reads; a name a read did not meet is dropped.
	gen uint32
	// stale asks the next look for a full read: never read, an overflow of the
	// kernel's queue, an operation that failed after taking a name.
	stale bool
}

// init readies an empty index of state, parsed by parse, with levels heaps.
func (d *dirIndex) init(state string, parse func(string) (nameValue, bool), levels int) {
	d.state, d.parse, d.stale = state, parse, true
	d.slots = make(map[string]indexSlot)
	d.heaps = make([]*heap.IndexedHeap[indexItem], 0, levels)
	//: one heap a level.
	for range levels {
		d.heaps = append(d.heaps, heap.NewIndexed(compareItems))
	}
}

// insert indexes name when its state's grammar reads it, at the current
// generation.
func (d *dirIndex) insert(name string) {
	parsed, ok := d.parse(name)
	//: a temporary, a stray file: never indexed, as a listing skipped it.
	if !ok {
		//: nothing to index.
		return
	}
	var bucket uint8
	//: a queued name's heap is its level's, in the order its digit sorts.
	if len(d.heaps) > 1 {
		digit, _ := nameStem(name)
		bucket = digit - '0'
	}
	handle := d.heaps[bucket].Push(indexItem{name: name, at: parsed.At})
	d.slots[name] = indexSlot{name: name, handle: handle, seen: d.gen, bucket: bucket}
}

// mark records that the current full read met an indexed name.
func (d *dirIndex) mark(slot indexSlot) {
	//: rewritten once a read.
	if slot.seen != d.gen {
		slot.seen = d.gen
		d.slots[slot.name] = slot
	}
}

// lookup finds the name raw holds without copying it out: a map read keyed
// by a conversion allocates nothing.
func (d *dirIndex) lookup(raw []byte) (slot indexSlot, ok bool) {
	slot, ok = d.slots[string(raw)]
	//: the place, or none.
	return slot, ok
}

// see is a full read meeting name: kept when indexed, indexed otherwise.
func (d *dirIndex) see(name string) {
	//: already indexed: marked.
	if slot, ok := d.slots[name]; ok {
		d.mark(slot)
		//: kept.
		return
	}
	d.insert(name)
}

// seeBytes is see for a name still in a read's buffer: a name already indexed
// costs no allocation, and only one of the grammar's suffix is copied out.
func (d *dirIndex) seeBytes(raw []byte) {
	//: already indexed: marked.
	if slot, ok := d.lookup(raw); ok {
		d.mark(slot)
		//: kept.
		return
	}
	//: what no grammar of a state reads costs no copy.
	if bytes.HasSuffix(raw, suffixMessageBytes) {
		d.insert(string(raw))
	}
}

// drop removes raw from the index, when it is there.
func (d *dirIndex) drop(raw []byte) {
	//: a name the index never held — ours, already taken, or a temporary.
	if slot, ok := d.lookup(raw); ok {
		d.heaps[slot.bucket].Remove(slot.handle)
		delete(d.slots, slot.name)
	}
}

// sweep drops every name the full read of generation gen did not meet.
func (d *dirIndex) sweep() {
	//: deleting while ranging is defined for a map.
	for name, slot := range d.slots {
		//: gone from the directory.
		if slot.seen != d.gen {
			d.heaps[slot.bucket].Remove(slot.handle)
			delete(d.slots, name)
		}
	}
}

// suffixMessageBytes is suffixMessage as the bytes a read's buffer holds.
var suffixMessageBytes = []byte(suffixMessage)

// dirOpener opens a state directory by its name under the queue's root: the
// broker's os.Root, the one thing a watch needs of it.
type dirOpener interface {
	Open(name string) (*os.File, error)
}

// fileIndex is what a file broker knows of its ready/ and inflight/
// directories between two reads of them (issue #811).
//
// Where the kernel names a directory's changes — inotify, on Linux, over a
// local filesystem — both directories are watched from the first Receive on:
// read whole once, then kept by the changes the kernel queued, which every
// look applies before it reads the index. A rename or an unlink is queued
// INSIDE the system call that makes it, before it returns, so a change
// another process finished before a Receive is in the queue that Receive
// drains: the first Receive after a publication sees it, without a margin.
// Everywhere else — another platform, a network or FUSE filesystem, no
// instance left, a watch lost — the index is read again under the listings'
// "racily clean" rule (file_listing.go), and a read is a mark and a sweep:
// no sort, no copy of a name already indexed where the platform's reader
// hands out its buffer (Linux).
//
// One mutex guards both indexes and the watch. A Receive takes a name out
// under it and renames outside it, so two goroutines of one broker never try
// the same message, and the rename still settles a race with any other.
type fileIndex struct {
	// watch is the kernel's change queue, nil while the directories are not
	// watched.
	watch *stateWatch
	// buf is the buffer a full read fills.
	buf *[direntBufferBytes]byte
	// why says why the directories are not watched, empty while they are.
	why string
	// ready and inflight are the two directories' indexes.
	ready    dirIndex
	inflight dirIndex
	mu       sync.Mutex
	// started is set by the first look, which tries to watch.
	started bool
	// unwatched keeps the directories from being watched: set by a test, to
	// run the platforms that have no watch, and by Close.
	unwatched bool
}

// of returns the index of state: ready/'s or inflight/'s.
func (x *fileIndex) of(state string) *dirIndex {
	//: the two states a Receive walks.
	if state == dirReady {
		//: the queued messages.
		return &x.ready
	}
	//: the leases.
	return &x.inflight
}

// start readies both indexes and tries to watch their directories. The
// caller holds the mutex.
func (x *fileIndex) start(b *fileBroker) {
	x.started = true
	x.ready.init(dirReady, parseReady, readyLevels)
	x.inflight.init(dirInflight, parseInflight, 1)
	x.buf = new([direntBufferBytes]byte)
	//: a test, or Close, ruled the watch out.
	if x.unwatched {
		x.why = "unwatched by the broker's owner"
		//: read under the listings' rule.
		return
	}
	x.watch, x.why = watchStates(b.root)
}

// sync brings the index of state up to what its directory holds — the
// queued changes applied where it is watched, a full read when the rule asks
// for one where it is not.
func (x *fileIndex) sync(b *fileBroker, state string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	//: the first look of the broker.
	if !x.started {
		x.start(b)
	}
	//: what the kernel queued since the last look, in order.
	if x.watch != nil {
		x.drain()
	}
	index := x.of(state)
	//: watched: read whole only when asked, or past the net's age.
	if x.watch != nil {
		//: what the kernel's changes cannot have kept.
		if index.stale || b.listed.wall().Sub(index.relistedAt) >= watchedRelistAge {
			//: QueueBackendFailed when the medium refuses.
			return x.relist(b, index)
		}
		//: current.
		return nil
	}
	//: QueueBackendFailed when the medium refuses.
	return x.syncUnwatched(b, index)
}

// syncUnwatched reads index's directory again unless the listings' rule
// says the last read still stands: its mtime unchanged and older than the
// read by racyMargin, the read younger than listingMaxAge.
func (x *fileIndex) syncUnwatched(b *fileBroker, index *dirIndex) error {
	//: BEFORE the stat and the read: a change the read misses is later still.
	listedAt := b.listed.wall()
	info, statErr := b.root.Stat(index.state)
	//: a state directory that vanished under a live broker is a real fault.
	if statErr != nil {
		//: QueueBackendFailed.
		return backendFailed("stat", index.state, statErr)
	}
	//: nothing changed since a read old enough to trust.
	if !index.stale && !index.stamp.listedAt.IsZero() && index.stamp.reusable(info.ModTime(), listedAt) {
		//: the index stands.
		return nil
	}
	//: the medium refused.
	if relistErr := x.relist(b, index); relistErr != nil {
		//: QueueBackendFailed.
		return relistErr
	}
	//: the stat's mtime, taken BEFORE the read, as namesOf records it.
	index.stamp = listingStamp{listedAt: listedAt, modTime: info.ModTime()}
	//: current.
	return nil
}

// relist reads index's directory whole into it: each name met kept or
// indexed, each name not met dropped.
func (x *fileIndex) relist(b *fileBroker, index *dirIndex) error {
	index.gen++
	dir, openErr := b.root.Open(index.state)
	//: a state directory that vanished under a live broker is a real fault.
	if openErr != nil {
		index.stale = true
		//: QueueBackendFailed, and the next look reads again.
		return backendFailed("readdir", index.state, openErr)
	}
	readErr := readEntries(dir, x.buf[:], index)
	closeErr := dir.Close()
	//: the read refused, or the descriptor would not close.
	if readErr != nil || closeErr != nil {
		//: half a read sweeps nothing: the next look reads again.
		index.stale = true
		//: QueueBackendFailed, the read's cause first.
		return backendFailed("readdir", index.state, cmp.Or(readErr, closeErr))
	}
	index.sweep()
	index.stale, index.relistedAt = false, b.listed.wall()
	//: current.
	return nil
}

// drain applies what the kernel queued; a watch that can no longer be read,
// or whose directory moved or vanished, is dropped, and both indexes are read
// under the listings' rule from then on. The caller holds the mutex.
func (x *fileIndex) drain() {
	lost, drainErr := x.watch.drain(x)
	//: the watch still answers for both directories.
	if !lost && drainErr == nil {
		//: applied.
		return
	}
	x.why = "the watch was lost: a state directory moved or vanished, or its queue failed"
	//: QueueBackendFailed would say nothing a Receive can act on: the
	//: directories are read from now on, as on a platform with no watch.
	if closeErr := x.watch.close(); closeErr != nil {
		x.why += "; closing it failed too"
	}
	x.watch = nil
	x.ready.stale, x.inflight.stale = true, true
}

// added is the kernel naming name as added to state. The caller holds the
// mutex.
func (x *fileIndex) added(state string, name []byte) {
	x.of(state).seeBytes(name)
}

// removed is the kernel naming name as gone from state. The caller holds the
// mutex.
func (x *fileIndex) removed(state string, name []byte) {
	x.of(state).drop(name)
}

// overflowed is the kernel saying its queue overflowed: changes were lost, so
// both directories are read whole at their next look. The caller holds the
// mutex.
func (x *fileIndex) overflowed() {
	x.ready.stale, x.inflight.stale = true, true
}

// head returns the first name of index's heap bucket, in the order a sorted
// listing walked it, without taking it.
func (x *fileIndex) head(index *dirIndex, bucket int) (name string, ok bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	item, _, found := index.heaps[bucket].Peek()
	//: the name, or none.
	return item.name, found
}

// take removes name from index's heap bucket when it is still that heap's
// first, and reports whether it did: false means another goroutine of this
// broker took it between head and here.
func (x *fileIndex) take(index *dirIndex, bucket int, name string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	item, _, found := index.heaps[bucket].Peek()
	//: somebody in this process took it first.
	if !found || item.name != name {
		//: not ours to act on.
		return false
	}
	index.heaps[bucket].Pop()
	delete(index.slots, name)
	//: ours: nobody else in this process tries it.
	return true
}

// invalidate asks the next look at index for a full read: an operation that
// failed after taking a name may have left it in the directory.
func (x *fileIndex) invalidate(index *dirIndex) {
	x.mu.Lock()
	defer x.mu.Unlock()
	index.stale = true
}

// unwatch drops the watch, if any, and keeps the directories from being
// watched again.
func (x *fileIndex) unwatch(why string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.unwatched = true
	//: nothing watched yet.
	if x.watch == nil {
		//: the first look will not watch.
		return nil
	}
	closeErr := x.watch.close()
	x.watch, x.why = nil, why
	x.ready.stale, x.inflight.stale = true, true
	//: the instance's descriptor released.
	return closeErr
}
