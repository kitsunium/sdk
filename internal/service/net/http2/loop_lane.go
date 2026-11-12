package http2

import (
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// connEntry's flags.
const (
	// entryLive is set between OnOpen and OnClose.
	entryLive entryFlags = 1 << iota
	// entryGoAway is set once a GOAWAY left on this connection.
	entryGoAway
	// entryEnded is set when the connection's output was refused.
	entryEnded
)

// requestState's flags.
const (
	// requestDeclared is set when a content-length was given.
	requestDeclared requestFlags = 1 << iota
	// requestHead is set when the request's method is HEAD.
	requestHead
	// requestRefused is set once an error answer went: its remaining bytes
	// are dropped.
	requestRefused
	// requestReading is set once OnBody has been called.
	requestReading
)

// The connection table's growth.
const (
	// entriesInitial is the first size of a ring's table.
	entriesInitial int = 16
	// entriesDoubleBelow is the table size below which entriesGrowth doubles.
	entriesDoubleBelow int = 1024
)

// defaultLaneLimits are the bounds a lane falls back on, every field the
// protocol's or the group's default, so resolving them cannot fail.
var defaultLaneLimits = corehttp2.LimitsValue{
	MaxStreams: corehttp2.DefaultMaxStreams, ReceivePerStream: corehttp2.DefaultReceivePerStream,
	ReceivePerConn: 4 * corehttp2.DefaultReceivePerStream, MaxFrame: corehttp2.DefaultMaxFrame,
	HeaderTable: corehttp2.DefaultHeaderTable, MaxHeaderList: corehttp2.DefaultMaxHeaderList,
}

// connEntry is one connection of a lane. An idle HTTP/2 connection holds this
// and nothing else: no request record, no table, no buffer.
type connEntry struct {
	// conn is the protocol state.
	conn Conn
	// requests is the head of the connection's list of open requests in the
	// lane's request table, noIndex when none.
	requests int32
	// buffered counts the body bytes the connection's open requests hold.
	buffered int32
	// gen is the generation of the connection the entry describes.
	gen uint32
	// activeAt is the lane batch the connection was last active in.
	activeAt uint32
	// kind is what the connection's first bytes made of it.
	kind connKind
	// flags are the entry's booleans.
	flags entryFlags
}

// entryFlags are a connEntry's booleans.
type entryFlags uint8

// live reports whether the entry holds an open connection.
func (e *connEntry) live() bool {
	//: set by OnOpen, cleared by OnClose.
	return e.flags&entryLive != 0
}

// requestState is one HTTP/2 request that outlives the OnData that opened it:
// a body still arriving, or an answer a worker owes. Pooled by the lane.
type requestState struct {
	// stream is the stream it answers.
	stream uint32
	// next links the connection's list, or the free list.
	next int32
	// req is the request as the handler sees it; its slices alias head.
	req corenet.HTTP1RequestValue
	// head holds the bytes req's slices alias.
	head []byte
	// body accumulates a body the handler takes whole.
	body []byte
	// uncredited counts body bytes not yet consumed for flow control.
	uncredited int32
	// slot is the worker slot answering it, noIndex when none.
	slot int32
	// wanted is the declared body length, from content-length.
	wanted int64
	// chunks counts the bytes given to OnBody.
	chunks int64
	// flags are the request's booleans.
	flags requestFlags
}

// requestFlags are a requestState's booleans.
type requestFlags uint8

// has reports whether the flags are all set.
func (r *requestState) has(f requestFlags) bool {
	//: every bit asked for.
	return r.flags&f == f
}

// workSlot is one pending answer: what the lane's worker fills. Slots are
// never moved, so the worker holds one by pointer, and the worker reads
// nothing else of the lane.
type workSlot struct {
	// idx is the slot's index in the table.
	idx uint32
	// gen is the slot's generation, moved on at each take.
	gen uint32
	// ref is the connection it answers.
	ref corenet.LoopConnRef
	// stream is the stream it answers.
	stream uint32
	// isHead is whether the request's method was HEAD.
	isHead bool
	// res is the answer the worker fills.
	res corenet.HTTP1ResponseValue
	// cancelled is set once the stream or connection ended: the worker skips
	// the work, and the answer is dropped.
	cancelled atomic.Bool
}

// deferredAnswer is one answer the batch's end completes.
type deferredAnswer struct {
	// res is the response the handler completes in EndBatch.
	res *corenet.HTTP1ResponseValue
	// ref is the connection it answers on.
	ref corenet.LoopConnRef
	// stream is the stream.
	stream uint32
	// isHead is whether the request was a HEAD.
	isHead bool
}

// write is one range of the batch's output.
type write struct {
	// ref is the connection it goes out on.
	ref corenet.LoopConnRef
	// off and end bound it in out.
	off, end int
}

// laneState is one lane's state. Only that lane's thread touches it, except
// where a field says.
type laneState struct {
	// lane is the protocol state of every HTTP/2 connection of the lane.
	lane *Lane
	// index is the lane's own number.
	index int
	// req is the request value every HTTP/2 request is decoded into.
	req corenet.HTTP1RequestValue
	// scratch holds decoded strings.
	scratch []byte
	// count numbers the lane's HTTP/2 requests, for HTTP1RequestValue.ID.
	count uint64
	// out is the batch's output.
	out []byte
	// jobOut is what a job's answer is written from.
	jobOut []byte
	// writes are the ranges of out, in order.
	writes []write
	// active are the connections the batch fed.
	active []corenet.LoopConnRef
	// entries is the connection table, by ring and slot.
	entries [][]connEntry
	// requests are the open requests by index; freeRequests their free list.
	requests     []*requestState
	freeRequests int32
	// completed are the requests answered this batch, returned at its end.
	completed []int32
	// deferred are the batch's deferred answers.
	deferred []deferredAnswer
	// free are the response values no request holds.
	free []*corenet.HTTP1ResponseValue
	// served counts the requests the batch handed to the handler.
	served int
	// errs counts the batch's HTTP/2 errors by corehttp2.ErrorType: the
	// codec's, taken at the batch's end, and the adapter's own answers.
	errs [corehttp2.ErrorTypes]uint64
	// outbound is the lane's queue to its worker, nil when nothing offloads.
	outbound corenet.LoopOutbound
	// slots are the worker slots, replaced under no lock since only this lane
	// grows it; the worker reads through it.
	slots atomic.Pointer[[]*workSlot]
	// freeSlots are the indexes of the slots no answer holds.
	freeSlots []uint32
	// post is the lane's LoopIO, published once for the drain's Post.
	post atomic.Pointer[postHolder]
	// goAwayDone is set when the drain's GOAWAY went out on this lane.
	goAwayDone atomic.Bool
}

// postHolder boxes a LoopIO for an atomic pointer.
type postHolder struct {
	// io is the lane's LoopIO.
	io corenet.LoopIO
}

// newLaneState builds lane i's state. A bound Resolve refuses is judged at
// Start, and the zero bounds resolve, so NewLane does not fail here.
func newLaneState(a *LoopHandler, i int) *laneState {
	resolved, err := a.limits.Resolve()
	//: a refused bound is the Start's to report (CheckSpill); a lane first
	//: seen here serves the defaults.
	if err != nil {
		resolved = defaultLaneLimits
	}
	st := &laneState{lane: newLaneResolved(resolved, a.maxBody, a.holdCap), index: i, freeRequests: noIndex}
	st.req.Lane = i
	//: the table starts empty, published before the worker can read it.
	var empty []*workSlot
	st.slots.Store(&empty)
	//: built.
	return st
}

// remember publishes the lane's LoopIO once, for the drain's Post.
func (st *laneState) remember(io corenet.LoopIO) {
	//: once per lane, the common path.
	if st.post.Load() != nil {
		//: already published.
		return
	}
	st.post.Store(&postHolder{io: io})
}

// entry returns ref's entry, growing the table on first sight of its ring or
// slot; a slot's next connection reuses the entry's Conn, whose arenas the
// last OnClose released.
func (st *laneState) entry(ref corenet.LoopConnRef) *connEntry {
	ring, slot := int(ref.Ring), int(ref.Slot)
	//: a ring seen for the first time.
	if ring >= len(st.entries) {
		st.entries = append(st.entries, make([][]connEntry, ring+1-len(st.entries))...)
	}
	//: a slot past the table: grown by doubling, paid a few times in a ring's life.
	if slot >= len(st.entries[ring]) {
		grown := make([]connEntry, max(slot+1, entriesGrowth(len(st.entries[ring]))))
		copy(grown, st.entries[ring])
		st.entries[ring] = grown
	}
	//: the connection's own, or its slot's previous tenant's, to be replaced by OnOpen.
	return &st.entries[ring][slot]
}

// entriesGrowth is the size a full table grows to: doubled, from sixteen.
func entriesGrowth(n int) int {
	//: the first sight of a ring.
	if n == 0 {
		//: enough for a small group.
		return entriesInitial
	}
	//: doubled while small, then a half more: the waste at a large table is
	//: what an idle connection pays (TestLoopHTTP2_idleMemory).
	if n < entriesDoubleBelow {
		//: doubled.
		return 2 * n
	}
	//: grown by half.
	return n + n/2
}

// take returns a zeroed response value for one request.
func (st *laneState) take() *corenet.HTTP1ResponseValue {
	//: none free: the pool grows by one, once.
	if len(st.free) == 0 {
		//: a fresh value is already zero.
		return &corenet.HTTP1ResponseValue{}
	}
	res := st.free[len(st.free)-1]
	st.free = st.free[:len(st.free)-1]
	*res = corenet.HTTP1ResponseValue{}
	//: the pooled value, cleared.
	return res
}

// give returns res to the pool. Its slices are the handler's and are dropped
// at the next take.
func (st *laneState) give(res *corenet.HTTP1ResponseValue) {
	st.free = append(st.free, res)
}

// takeRequest returns a request record, cleared, linked at the head of e's
// list, and its index.
func (st *laneState) takeRequest(e *connEntry, stream uint32) (int32, *requestState) {
	idx := st.freeRequests
	//: the free list first, then a new record.
	if idx != noIndex {
		st.freeRequests = st.requests[idx].next
	} else {
		st.requests = append(st.requests, &requestState{})
		idx = int32(len(st.requests) - 1)
	}
	r := st.requests[idx]
	r.resetKeepingBuffers(stream, e.requests)
	e.requests = idx
	//: linked.
	return idx, r
}

// resetKeepingBuffers clears r for a new request, keeping the backing of its
// head and body so a churn of requests does not allocate them again.
func (r *requestState) resetKeepingBuffers(stream uint32, next int32) {
	head, body := r.head[:0], r.body[:0]
	*r = requestState{stream: stream, next: next, slot: noIndex, head: head, body: body}
}

// findRequest returns the record of stream on e and its predecessor, or noIndex.
func (st *laneState) findRequest(e *connEntry, stream uint32) (idx, prev int32) {
	prev = noIndex
	//: at most MaxStreams steps.
	for idx = e.requests; idx != noIndex; idx = st.requests[idx].next {
		//: found.
		if st.requests[idx].stream == stream {
			//: with its predecessor, for the unlink.
			return idx, prev
		}
		prev = idx
	}
	//: none.
	return noIndex, prev
}

// unlinkRequest removes record idx from e's list, without freeing it.
func (st *laneState) unlinkRequest(e *connEntry, idx, prev int32) {
	r := st.requests[idx]
	//: the head has no predecessor to relink.
	if prev == noIndex {
		e.requests = r.next
	} else {
		st.requests[prev].next = r.next
	}
}

// release returns record idx to the free list at the batch's end; a body or
// head a large request grew is dropped rather than kept, so the pool's
// memory follows the common request.
func (st *laneState) release(idx int32) {
	r := st.requests[idx]
	//: a big buffer goes back to the heap.
	if cap(r.body) > holdKeepMax {
		r.body = nil
	}
	//: the head's too.
	if cap(r.head) > holdKeepMax {
		r.head = nil
	}
	r.next = st.freeRequests
	st.freeRequests = idx
}

// finishRequest drops record idx from e, crediting what it never consumed and
// the bytes it held, and queues it for the batch's end, its bytes still valid
// for a handler that deferred its answer.
func (st *laneState) finishRequest(e *connEntry, idx, prev int32) {
	r := st.requests[idx]
	//: bytes the connection held back for this request.
	if r.uncredited > 0 {
		st.lane.Consume(&e.conn, r.stream, int(r.uncredited))
		r.uncredited = 0
	}
	e.buffered -= int32(len(r.body))
	st.unlinkRequest(e, idx, prev)
	st.completed = append(st.completed, idx)
}

// closeRequests abandons every open request of e: an OnBody handler is told,
// a worker's answer is cancelled.
func (st *laneState) closeRequests(a *LoopHandler, e *connEntry) {
	//: every record on the connection's list.
	for idx := e.requests; idx != noIndex; {
		r := st.requests[idx]
		next := r.next
		//: a body the handler was reading as it arrived.
		if r.has(requestReading) && a.aborter != nil {
			a.aborter.AbortBody(&r.req)
		}
		//: an answer a worker still owes: dropped when it comes back.
		if r.slot != noIndex {
			(*st.slots.Load())[r.slot].cancelled.Store(true)
		}
		st.release(idx)
		idx = next
	}
	e.requests, e.buffered = noIndex, 0
}
