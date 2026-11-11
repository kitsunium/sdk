package http2

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// The connection state machine's numbers, RFC 9113 and ADR 0220.
const (
	// clientPrefaceLen is the length of the client connection preface, §3.4.
	clientPrefaceLen int = 24
	// noIndex marks the end of a stream list or a table not taken.
	noIndex int32 = -1
	// defaultHoldCap is the most DATA one connection may hold unsent: the
	// loop's OutputCap default, what the adapter names when it has none.
	defaultHoldCap int = 1 << 20
	// controlPerBatch is how many frames that ask for an answer, or do no
	// useful work, one batch may carry on one connection: past it the
	// connection is ending the peer's flood, RFC 9113 §10.5.
	controlPerBatch int32 = 1024
	// resetBudgetFactor sizes the Rapid Reset budget, in MaxStreams: the peer
	// resets at most that many unfinished streams net of those it let finish.
	resetBudgetFactor int32 = 2
	// maxContinuationFrames bounds the frames of one field block: past them
	// it is a CONTINUATION flood, however few bytes each carries.
	maxContinuationFrames int32 = 32
	// blockBoundFactor sizes the bytes of a whole field block, in the
	// header-list ceiling: Huffman coding can only expand a string so far.
	blockBoundFactor int = 4
	// creditDivisor is the fraction of a window consumed before a WINDOW_UPDATE
	// is written for it: small acknowledgments never leave.
	creditDivisor int32 = 4
	// holdKeepMax is the largest buffer the lane keeps for reuse.
	holdKeepMax int = 1 << 16
	// frameKnownMax is the largest frame type this package dispatches on.
	frameKnownMax corehttp2.FrameType = corehttp2.FrameContinuation
	// settingsAckLen is a SETTINGS ACK's payload length.
	settingsAckLen int = 0
	// settingLenLocal is the wire size of one SETTINGS parameter.
	settingLenLocal int = 6
)

// tableChunk is how many tables one chunk of the table arena holds.
const tableChunk int32 = 64

const (
	// flagPreface says the client preface has been read.
	flagPreface connFlags = 1 << iota
	// flagSettings says the client's first SETTINGS has been read.
	flagSettings
	// flagGoAway says a GOAWAY left: no new stream is admitted.
	flagGoAway
	// flagPeerGoAway says the client sent GOAWAY: it opens no new stream.
	flagPeerGoAway
	// flagClosing says the connection ended with an error: nothing more is read.
	flagClosing
)

const (
	// streamRemoteClosed says the peer's half is closed (END_STREAM received).
	streamRemoteClosed streamFlags = 1 << iota
	// streamLocalClosed says our half is closed: END_STREAM sent.
	streamLocalClosed
	// streamOverBody says the body ran past the bound: nothing more is kept.
	streamOverBody
	// streamHoldEnd says END_STREAM follows the held bytes.
	streamHoldEnd
)

const (
	// EventHeaders is a whole field block for a stream, to decode with Lane.Decode.
	EventHeaders EventKind = iota
	// EventData is a stream's DATA payload, padding removed.
	EventData
	// EventReset is a stream the peer or the state machine ended.
	EventReset
	// EventGoAway is the peer's GOAWAY; it opens no new stream.
	EventGoAway
)

const (
	// EventEndStream reports the stream's last frame arrived with this event.
	EventEndStream EventFlags = 1 << iota
	// EventDiscard reports an EventHeaders whose stream was refused: decoded only
	// to keep the connection's table in step, RFC 9113 §4.3, and dropped.
	EventDiscard
	// EventOverBody reports an EventData whose bytes passed the body bound: the
	// adapter refuses the request (413) and the codec keeps nothing more.
	EventOverBody
)

// clientPrefaceText is the client connection preface, RFC 9113 §3.4.
const clientPrefaceText string = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// clientPrefaceBytes is the client connection preface, RFC 9113 §3.4.
var clientPrefaceBytes = []byte(clientPrefaceText)

// connFlags are the booleans of a Conn, packed to keep it pointer-free and small.
type connFlags uint8

// streamFlags are the booleans of a streamState.
type streamFlags uint8

// Conn is one connection's protocol state: windows, settings and bounds, with
// no pointer in it, so a slice of them holds nothing the collector scans. The
// adapter keeps one per (ring, slot) and hands it to every call of its Lane;
// zero is not ready, Lane.Open makes it a connection.
type Conn struct {
	// decoder is the connection's HPACK table's bookkeeping.
	decoder Decoder
	// peerWindow is what the peer may send on the connection.
	peerWindow int32
	// sendWindow is what we may send on the connection.
	sendWindow int32
	// peerCredit is consumed bytes not yet acknowledged on the connection.
	peerCredit int32
	// initialSend is the stream send window a new stream starts with: the
	// peer's INITIAL_WINDOW_SIZE.
	initialSend int32
	// peerMaxFrame is the largest frame the peer accepts.
	peerMaxFrame int32
	// head is the newest open stream, noIndex when none: the list by index.
	head int32
	// open counts the streams on the list.
	open int32
	// lastStream is the highest stream id the peer has used: a new stream
	// must be above it, §5.1.1, refused or not.
	lastStream uint32
	// lastOpen is the highest stream id admitted: what GOAWAY names, §6.8.
	lastOpen uint32
	// table is the dynamic table's block in the lane's arena; noIndex until
	// the connection's first field block.
	table int32
	// hold is the connection's held-bytes buffer in the lane's arena; noIndex
	// when nothing is held.
	hold int32
	// held counts the bytes held unsent, all streams.
	held int32
	// resets is what is left of the Rapid Reset budget.
	resets int32
	// control counts this batch's frames that ask for an answer or do no work.
	control int32
	// epoch is the batch control was counted in.
	epoch uint32
	// scanFrames is how many CONTINUATION frames of the field block waiting
	// at the front of the input are already known good.
	scanFrames int32
	// scanOff is where they end, relative to the HEADERS frame's first byte.
	scanOff int32
	// scanBlock counts the field block's bytes seen so far.
	scanBlock int32
	// flags are the connection's booleans.
	flags connFlags
}

// has reports whether the flags are all set.
func (c *Conn) has(f connFlags) bool {
	return c.flags&f == f
}

// set raises the flags.
func (c *Conn) set(f connFlags) {
	c.flags |= f
}

// streamState is one open stream, indexed by Conn.head and threaded by next:
// a pointer-free arena per lane.
type streamState struct {
	// id is the stream's identifier.
	id uint32
	// next links the stream to an older one on the connection, or to the free list.
	next int32
	// peerWindow is what the peer may still send on the stream.
	peerWindow int32
	// sendWindow is what we may still send on the stream.
	sendWindow int32
	// peerCredit is consumed bytes not yet acknowledged on the stream.
	peerCredit int32
	// holdOff locates the stream's held bytes in the connection's buffer.
	holdOff int32
	// holdLen counts them.
	holdLen int32
	// bodyLen counts the body bytes received so far.
	bodyLen int32
	// flags are the stream's booleans.
	flags streamFlags
}

// has reports whether the flags are all set.
func (s *streamState) has(f streamFlags) bool {
	return s.flags&f == f
}

// set raises the flags.
func (s *streamState) set(f streamFlags) {
	s.flags |= f
}

// EventKind says what an Event is about.
type EventKind uint8

// EventFlags are an Event's booleans: EventEndStream, EventDiscard and
// EventOverBody.
type EventFlags uint8

// Event is what Feed reports for the adapter's batch: a field block to decode,
// body bytes to keep, a stream that ended. The bytes alias Feed's buf, or the
// lane's scratch for a block that came across CONTINUATION frames; both are
// valid until the batch's end.
type Event struct {
	// Kind says what the event is.
	Kind EventKind
	// Stream is the stream concerned; zero for an EventGoAway.
	Stream uint32
	// Data is the field block of an EventHeaders, or the body bytes of an
	// EventData.
	Data []byte
	// Flags carries what the frame said beside its bytes.
	Flags EventFlags
	// Code is the error code an EventReset or EventGoAway carried.
	Code corehttp2.ErrCode
}

// holdBuffer is the bytes held unsent for one connection: DATA waiting for
// flow-control credit, bounded by the lane's holdCap.
type holdBuffer struct {
	// buf holds the bytes, compacted once more than half of it is dead.
	buf []byte
	// dead counts the bytes in buf no stream refers to.
	dead int32
}

// Lane is one lane's protocol state: the streams, tables and held bytes of
// every connection of the lane, in arenas with no per-connection pointer. A
// lane is used by one thread; nothing here takes a lock. One Lane serves all
// the Conns of a loop.
type Lane struct {
	// limits are the resolved bounds of the group.
	limits corehttp2.LimitsValue
	// errs counts the errors Feed answered, by corehttp2.ErrorType, until
	// TakeErrors.
	errs [corehttp2.ErrorTypes]uint64
	// maxBody is the body bound a request is refused past (413).
	maxBody int32
	// holdCap is the most DATA one connection may hold unsent.
	holdCap int32
	// streams is the arena of stream states.
	streams []streamState
	// freeStream is the free list's head in streams.
	freeStream int32
	// tables hands out the HPACK tables.
	tables tableArena
	// holds are the held-bytes buffers, by Conn.hold.
	holds []holdBuffer
	// freeHolds are the holds no connection uses.
	freeHolds []int32
	// events are the last Feed's events.
	events []Event
	// join is the current batch's reassembled field blocks.
	join []byte
	// epoch numbers the batches, from one.
	epoch uint32
}

// tableArena hands out HPACK tables of one size from chunks, pointer-free.
type tableArena struct {
	// size is one table's bytes.
	size int
	// chunks are the arena's chunks, tableChunk tables each.
	chunks [][]byte
	// free are the tables on the free list.
	free []int32
	// next is the first table never handed out.
	next int32
}

// get returns a free table's id, growing the arena when none is free.
func (a *tableArena) get() int32 {
	//: the free list is the fast path of a slot's churn.
	if n := len(a.free); n > 0 {
		id := a.free[n-1]
		a.free = a.free[:n-1]
		return id
	}
	//: the last chunk is full: another one.
	if a.next == int32(len(a.chunks))*tableChunk {
		a.chunks = append(a.chunks, make([]byte, int(tableChunk)*a.size))
	}
	id := a.next
	a.next++
	return id
}

// mem returns the memory of table id.
func (a *tableArena) mem(id int32) []byte {
	block := int(id%tableChunk) * a.size
	return a.chunks[id/tableChunk][block : block+a.size]
}

// put returns table id to the free list.
func (a *tableArena) put(id int32) {
	a.free = append(a.free, id)
}

// NewLane returns one lane's protocol state for the bounds v and a body bound
// of maxBody bytes; holdCap is the most DATA one connection may hold unsent,
// zero selecting one mebibyte. A bound it cannot honour refuses with
// corehttp2.LimitsRefused, naming the bound.
func NewLane(v corehttp2.LimitsValue, maxBody int64, holdCap int) (*Lane, error) {
	resolved, err := v.Resolve()
	//: a misconfigured group fails at Start, naming the bound.
	if err != nil {
		return nil, err
	}
	return newLaneResolved(resolved, maxBody, holdCap), nil
}

// newLaneResolved returns one lane's state for bounds already resolved.
func newLaneResolved(resolved corehttp2.LimitsValue, maxBody int64, holdCap int) *Lane {
	//: zero is the default, never unbounded.
	if holdCap == 0 {
		holdCap = defaultHoldCap
	}
	return &Lane{
		limits:     resolved,
		maxBody:    int32(min(maxBody, int64(corehttp2.MaxWindow))),
		holdCap:    int32(min(holdCap, corehttp2.MaxWindow)),
		freeStream: noIndex,
		tables:     tableArena{size: TableMem(resolved.HeaderTable)},
		epoch:      1,
	}
}

// Open makes c a new connection on this lane; a Conn may be reused for a new
// connection once Close released it.
func (l *Lane) Open(c *Conn) {
	*c = Conn{
		decoder:      NewDecoder(l.limits.HeaderTable, l.limits.MaxHeaderList),
		peerWindow:   int32(corehttp2.DefaultWindow),
		sendWindow:   int32(corehttp2.DefaultWindow),
		initialSend:  int32(corehttp2.DefaultWindow),
		peerMaxFrame: int32(corehttp2.DefaultMaxFrame),
		head:         noIndex,
		table:        noIndex,
		hold:         noIndex,
		resets:       resetBudgetFactor * int32(l.limits.MaxStreams),
	}
}

// Close releases what c holds in the lane's arenas: its streams, its table
// and its held bytes. The connection's slot is reusable once Open is called
// again.
func (l *Lane) Close(c *Conn) {
	//: every stream goes back on the free list.
	for idx := c.head; idx != noIndex; {
		next := l.streams[idx].next
		l.freeStreamSlot(idx)
		idx = next
	}
	//: the table is reused by the lane, not given back to the heap.
	if c.table != noIndex {
		l.tables.put(c.table)
	}
	l.releaseHold(c)
	c.head, c.table, c.hold = noIndex, noIndex, noIndex
	c.open, c.held = 0, 0
}

// Events returns the events of the last Feed, in the order the frames came.
func (l *Lane) Events() []Event {
	return l.events
}

// EndBatch ends the batch: the scratch of reassembled field blocks and the
// control budgets reset. Call it once per batch, after the batch's last
// Feed, once every byte it aliased has been handled.
func (l *Lane) EndBatch() {
	l.join = l.join[:0]
	l.epoch++
}

// TakeErrors adds into dst, indexed by corehttp2.ErrorType, the errors Feed
// answered on the lane's connections since the last call — each RST_STREAM
// and GOAWAY it wrote with an error code — and clears them; a dst shorter
// than corehttp2.ErrorTypes takes the types that fit. What the caller
// answers itself, with Abort, ResetStream or GoAway, is the caller's to count.
func (l *Lane) TakeErrors(dst []uint64) {
	//: each type that fits.
	for i := range min(len(dst), len(l.errs)) {
		dst[i] += l.errs[i]
		l.errs[i] = 0
	}
}

// countErr counts an error Feed answers with code.
func (l *Lane) countErr(code corehttp2.ErrCode) {
	l.errs[corehttp2.ErrorTypeOf(code)]++
}

// Decode decodes a field block of an EventHeaders into req; scratch receives
// the bytes not aliased from the block and is returned grown. A block the
// table cannot survive refuses with CompressionFailed or HeadersTooLarge: the
// caller ends the connection with Abort. A FieldMalformed leaves the table
// intact, and only the stream is refused.
func (l *Lane) Decode(c *Conn, block, scratch []byte, req *corenet.HTTP1RequestValue) ([]byte, error) {
	//: the table is taken at the connection's first block, never before.
	if c.table == noIndex {
		c.table = l.tables.get()
	}
	return c.decoder.Decode(l.tables.mem(c.table), block, scratch, req)
}

// Closing reports whether c ended with an error; the connection closes once
// dst has been written.
func (*Lane) Closing(c *Conn) bool {
	return c.has(flagClosing)
}

// Done reports whether c can close now: a GOAWAY went one way or the other
// and no stream is left open. The answer is the adapter's to act on, after
// writing dst.
func (*Lane) Done(c *Conn) bool {
	return c.flags&(flagGoAway|flagPeerGoAway) != 0 && c.open == 0
}
