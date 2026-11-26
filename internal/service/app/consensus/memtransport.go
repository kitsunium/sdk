package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// memQueueFrames is the depth of each per-lane queue of the memory double:
// deep enough that a test's burst is not dropped by accident, finite so a
// stalled receiver still loses frames the way a real lane does (D2).
const memQueueFrames int = 4096

// MemoryNetwork connects in-memory transports, the double of D2: frames are
// copied, delivered on per-lane queues that drop when full, and links can be
// cut in either direction.
type MemoryNetwork struct {
	mu    sync.Mutex
	nodes map[coreconsensus.NodeID]*MemoryTransport
	cut   map[[2]coreconsensus.NodeID]bool
	// admission, set by the package's own tests before any node starts,
	// models the TCP transport's: a frame or a snapshot is delivered only
	// between nodes each of which admits the other, under its own
	// configuration, by ID and pinned key (D4). Off, as the double has
	// always run, every node reaches every other (#542).
	admission bool
}

// NewMemoryNetwork returns an empty network.
func NewMemoryNetwork() *MemoryNetwork {
	return &MemoryNetwork{nodes: map[coreconsensus.NodeID]*MemoryTransport{}, cut: map[[2]coreconsensus.NodeID]bool{}}
}

// NewMemoryTransport returns the end of the network n that node id sends and
// receives through: created on the first call for that id, the same one on
// every later call, so a node restarted on the network keeps its links.
func NewMemoryTransport(n *MemoryNetwork, id coreconsensus.NodeID) *MemoryTransport {
	n.mu.Lock()
	defer n.mu.Unlock()
	if t, ok := n.nodes[id]; ok {
		return t
	}
	t := &MemoryTransport{
		net: n, id: id,
		ctrl: make(chan memFrame, memQueueFrames), data: make(chan memFrame, memQueueFrames),
	}
	n.nodes[id] = t
	return t
}

// Cut cuts, or restores, the link from a to b.
func (n *MemoryNetwork) Cut(a, b coreconsensus.NodeID, cut bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if cut {
		n.cut[[2]coreconsensus.NodeID{a, b}] = true
	} else {
		delete(n.cut, [2]coreconsensus.NodeID{a, b})
	}
}

// Isolate cuts, or restores, every link of id in both directions.
func (n *MemoryNetwork) Isolate(id coreconsensus.NodeID, cut bool) {
	n.mu.Lock()
	ids := slices.Collect(maps.Keys(n.nodes))
	n.mu.Unlock()
	for _, x := range ids {
		if x != id {
			n.Cut(id, x, cut)
			n.Cut(x, id, cut)
		}
	}
}

// route is the transport a frame from one node to another reaches, or nil
// when that direction of the link is cut or the destination never joined.
func (n *MemoryNetwork) route(from, to coreconsensus.NodeID) *MemoryTransport {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cut[[2]coreconsensus.NodeID{from, to}] {
		return nil
	}
	return n.nodes[to]
}

type memFrame struct {
	from  coreconsensus.NodeID
	frame []byte
}

// MemoryTransport is one node's end of a MemoryNetwork, the in-memory
// double of D2. NewMemoryTransport returns it; Close stops its delivery
// until the next Bind.
type MemoryTransport struct {
	net     *MemoryNetwork
	id      coreconsensus.NodeID
	mu      sync.Mutex
	deliver func(coreconsensus.AuthenticatedPeerValue, []byte)
	offer   func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue
	install func(coreconsensus.InstallParams) error
	join    func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code)
	joinR   func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)
	ctrl    chan memFrame
	data    chan memFrame
	cur     *memRun
	// groups are the admitted members per group, groupsAt the index each
	// of their records is as of, members their union (admittedUnion, #742).
	groups   map[coreconsensus.GroupID][]coreconsensus.MemberValue
	groupsAt map[coreconsensus.GroupID][]uint64
	members  []coreconsensus.MemberValue
	// hold keeps the JOIN answer's configuration and release (#632).
	hold joinHold
	// maxOffer bounds the snapshot offer this end accepts, encoded as the
	// TCP transport sends it; 0 is the TCP transport's default
	// MaxFrameBytes. Set by the package's own tests only (#634).
	maxOffer int
	// warn is the node's log, for an offer refused before the node judged
	// it, as on the TCP transport (#634).
	warn func(msg string, attrs ...corelogger.AttrValue)
	// membership reads the configuration the bound node runs with, for a
	// member that asks it (#763); nil answers nothing.
	membership func() (coreconsensus.MembershipValue, bool)
	// predatesMembership answers no read of the configuration, as a member
	// built before it. Set by the package's own tests only.
	predatesMembership bool
}

// memRun is one delivery goroutine's life: Close closes stop, once, and
// waits on done, which the goroutine closes as it returns. Each Bind after a
// Close starts a run of its own, so a Close never waits on — or stops — a
// run a later Bind started.
type memRun struct {
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
	doneOnce sync.Once
}

// end stops the run; a second call is a no-op.
func (r *memRun) end() { r.once.Do(func() { close(r.stop) }) }

// Send implements Transport: it never blocks and drops when the queue is
// full.
func (t *MemoryTransport) Send(to coreconsensus.NodeID, frame []byte) {
	//: a destination this end does not admit is sent nothing.
	if !t.net.admitted(t, to) {
		return
	}
	lane, err := wire.Lane(frame)
	t.net.enqueue(t.id, to, frame, err == nil && lane == coreconsensus.LaneControl)
}

// enqueue queues a copy of frame from one node to another, on its control
// lane or its data lane, unless that direction of the link is cut or the
// destination never joined. The cut is read and the frame queued in one
// section under the network's lock: a Send that took its destination, then
// queued once the lock was released, queued its frame after a Cut or an
// Isolate that had returned, and after the drain of the stopped node's
// queues that followed, which delivered it at the node's restart (#1564).
// The queue never blocks, a full lane dropping the frame, so the lock is
// never held waiting on a receiver.
func (n *MemoryNetwork) enqueue(from, to coreconsensus.NodeID, frame []byte, control bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dst := n.nodes[to]
	//: a cut link, or a destination that never joined, is sent nothing.
	if n.cut[[2]coreconsensus.NodeID{from, to}] || dst == nil {
		return
	}
	q := dst.data
	//: the frame header names the control lane.
	if control {
		q = dst.ctrl
	}
	select {
	//: queued for the destination's delivery goroutine.
	case q <- memFrame{from: from, frame: bytes.Clone(frame)}:
	//: the lane is full: the frame is dropped, as a real lane drops it.
	default:
	}
}

// Stream implements Transport: offer, accept, then the stream from the
// accepted offset.
func (t *MemoryTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	dst := t.net.route(t.id, to)
	if dst == nil {
		return origin(coreconsensus.PeerRejected, errs.String("why", "unreachable"))
	}
	if !t.net.admitted(t, to) || !t.net.admitted(dst, t.id) {
		return origin(coreconsensus.PeerRejected, errs.String("why", "not admitted"))
	}
	dst.mu.Lock()
	of, in, warn, bound := dst.offer, dst.install, dst.warn, dst.maxOffer
	dst.mu.Unlock()
	if bound <= 0 {
		bound = defaultMaxFrameBytes
	}
	if n := len(encodeOffer(offer)); n > bound {
		// The receiver refuses it as the TCP one does, and the sender sees
		// its stream end unanswered (#634).
		offerRefused(warn, t.id, bound, origin(coreconsensus.FrameInvalid, errs.Int("length", n)))
		return origin(coreconsensus.FrameInvalid, errs.String("what", "install accept"))
	}
	if of == nil {
		return origin(coreconsensus.PeerRejected, errs.String("why", "unbound"))
	}
	peer := coreconsensus.AuthenticatedPeerValue{ID: t.id}
	acc := of(peer, offer)
	if acc.Held {
		return nil
	}
	if acc.Refused != 0 {
		return origin(sentinelOf(acc.Refused))
	}
	rc, err := open(acc.Offset)
	if err != nil {
		return err
	}
	body := &streamCancelWatch{ReadCloser: rc, ctx: ctx}
	ierr := in(coreconsensus.InstallParams{Ctx: ctx, From: peer, Offer: offer, Offset: acc.Offset, Body: body})
	//: a stream cut by its cancellation ends as cancelled, as a closed TCP connection does,
	// never as the receiver's corrupt verdict, which made the sender read its snapshot back.
	if ierr != nil && body.cancelled.Load() {
		abandon(rc.Close())
		return ctx.Err()
	}
	// The install's verdict wins; a Close failure of the stream it read is
	// reported only when the install itself succeeded.
	if cerr := rc.Close(); cerr != nil && ierr == nil {
		return cerr
	}
	return ierr
}

// streamCancelWatch notes that a read of the stream failed while its context was
// cancelled, which tells a cancelled stream from one that ended short.
type streamCancelWatch struct {
	io.ReadCloser
	ctx       context.Context
	cancelled atomic.Bool
}

// Read reads the stream and records a failure it owes to the cancellation.
func (w *streamCancelWatch) Read(p []byte) (int, error) {
	k, err := w.ReadCloser.Read(p)
	//: only a failed read under a cancelled context counts; a clean EOF never does.
	if err != nil && !errors.Is(err, io.EOF) && w.ctx.Err() != nil {
		w.cancelled.Store(true)
	}
	return k, err
}

// Bind implements Transport. The first Bind after construction or Close
// launches the background delivery goroutine of its own run, which ends
// when Close stops that run; a later Bind launches a new one.
func (t *MemoryTransport) Bind(group coreconsensus.GroupID,
	deliver func(coreconsensus.AuthenticatedPeerValue, []byte),
	offer func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue,
	install func(coreconsensus.InstallParams) error,
	join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code),
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.deliver, t.offer, t.install, t.join = deliver, offer, install, join
	if t.cur == nil {
		t.cur = &memRun{stop: make(chan struct{}), done: make(chan struct{})}
		go t.run(t.cur)
	}
	return nil
}

// run is the background delivery goroutine Bind launches: it prefers the
// control lane over the data lane, as the TCP transport does, and ends when
// the stop channel of its run is closed by Close.
func (t *MemoryTransport) run(r *memRun) {
	defer r.doneOnce.Do(func() { close(r.done) })
	stop := r.stop
	for {
		var f memFrame
		select {
		case <-stop:
			return
		case f = <-t.ctrl:
		default:
			select {
			case <-stop:
				return
			case f = <-t.ctrl:
			case f = <-t.data:
			}
		}
		t.deliverFrame(f)
	}
}

// deliverFrame hands one frame to the bound deliver callback, unless the
// admission model is on and this node does not admit its sender: the TCP
// transport closes the connection of a key it does not admit, and the
// frame never reaches the node.
func (t *MemoryTransport) deliverFrame(f memFrame) {
	if !t.net.admitted(t, f.from) {
		return
	}
	t.mu.Lock()
	d := t.deliver
	t.mu.Unlock()
	if d != nil {
		d(coreconsensus.AuthenticatedPeerValue{ID: f.from}, f.frame)
	}
}

// BindJoinReason implements JoinReasonBinder.
func (t *MemoryTransport) BindJoinReason(_ coreconsensus.GroupID, join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)) {
	t.mu.Lock()
	t.joinR = join
	t.mu.Unlock()
}

// SetMembership implements Transport: the admitted members are the union
// over the groups, as the TCP transport computes them.
func (t *MemoryTransport) SetMembership(group coreconsensus.GroupID, admitted []coreconsensus.MemberValue) {
	t.setMembershipAt(group, admitted, nil)
}

// setMembershipAt is SetMembership with the index each record is as of,
// ranked as the TCP transport ranks it (membershipSetter, #742).
func (t *MemoryTransport) setMembershipAt(group coreconsensus.GroupID, admitted []coreconsensus.MemberValue, at []uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.groups == nil {
		t.groups = map[coreconsensus.GroupID][]coreconsensus.MemberValue{}
		t.groupsAt = map[coreconsensus.GroupID][]uint64{}
	}
	if len(admitted) == 0 {
		delete(t.groups, group)
		delete(t.groupsAt, group)
	} else {
		t.groups[group] = slices.Clone(admitted)
		t.groupsAt[group] = slices.Clone(at)
	}
	t.members = admittedUnion(t.groups, t.groupsAt)
}

// bindWarn implements bindWarner.
func (t *MemoryTransport) bindWarn(warn func(msg string, attrs ...corelogger.AttrValue)) {
	t.mu.Lock()
	t.warn = warn
	t.mu.Unlock()
}

// bindMembership binds read, which answers a member's read of the
// configuration the node runs with, as the TCP transport does (#763).
func (t *MemoryTransport) bindMembership(read func() (coreconsensus.MembershipValue, bool)) {
	t.mu.Lock()
	t.membership = read
	t.mu.Unlock()
}

// AskMembership reads, from member m, the configuration it runs with, as
// the TCP transport does (#763): m is reached when the link is up both
// ways, authenticated under the key the memory transport gives it, and
// answers only a node it admits, while a node runs bound to its transport.
func (t *MemoryTransport) AskMembership(_ context.Context, m coreconsensus.MemberValue) (coreconsensus.MembershipValue, error) {
	dst := t.net.route(t.id, m.ID)
	if dst == nil || t.net.route(m.ID, t.id) == nil {
		return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "unreachable"))
	}
	if m.SPKI != memSPKI(m.ID) {
		return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "key does not match the member's pin"))
	}
	if !t.net.admitted(dst, t.id) {
		return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "not admitted"))
	}
	dst.mu.Lock()
	read, old := dst.membership, dst.predatesMembership
	dst.mu.Unlock()
	if read == nil || old {
		return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "unanswered"))
	}
	conf, ok := read()
	if !ok {
		return coreconsensus.MembershipValue{}, origin(coreconsensus.PeerRejected, errs.String("why", "unanswered"))
	}
	return conf, nil
}

// releaseJoinAnswer drops the JOIN answer's group once the node is
// admitted, as the TCP transport does (#552).
func (t *MemoryTransport) releaseJoinAnswer() {
	t.hold.release(func() { t.SetMembership(joinGroup, nil) })
}

// admitted reports whether at admits peer: always without the admission
// model, else when one of its admitted members is peer under peer's key.
func (n *MemoryNetwork) admitted(at *MemoryTransport, peer coreconsensus.NodeID) bool {
	n.mu.Lock()
	on := n.admission
	n.mu.Unlock()
	if !on {
		return true
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	return slices.ContainsFunc(at.members, func(m coreconsensus.MemberValue) bool {
		return m.ID == peer && m.SPKI == memSPKI(peer)
	})
}

// Join implements Joiner over the network, as the TCP transport does.
func (t *MemoryTransport) Join(ctx context.Context, cfg coreconsensus.JoinConfig) (coreconsensus.PositionValue, error) {
	gen := t.hold.begin()
	pos, err := t.joinVia(ctx, &cfg, gen)
	if err == nil {
		t.hold.keep(gen, &cfg)
	}
	return pos, err
}

// refreshJoin sends the JOIN again as the TCP transport does (#632).
func (t *MemoryTransport) refreshJoin(ctx context.Context) (bool, error) {
	cfg, gen, ok := t.hold.again()
	if !ok {
		return false, nil
	}
	t.mu.Lock()
	before := slices.Clone(t.groups[joinGroup])
	t.mu.Unlock()
	if _, err := t.joinVia(ctx, &cfg, gen); err != nil {
		return false, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return !sameAnswer(before, t.groups[joinGroup]), nil
}

// joinVia sends the JOIN of cfg to every other transport of the network in
// turn, the leader a member refusing it NOT_LEADER names next (#1422), and
// installs the answer in the hold's generation gen.
func (t *MemoryTransport) joinVia(ctx context.Context, cfg *coreconsensus.JoinConfig, gen uint64) (coreconsensus.PositionValue, error) {
	req := coreconsensus.JoinRequestValue{
		Cluster: cfg.Stamp.Log.Cluster, ID: cfg.Stamp.ID, Incarnation: cfg.Stamp.Incarnation,
		Slot: cfg.Stamp.Slot, Address: cfg.Address, MaxSnapshotBytes: max(cfg.MaxSnapshotBytes, 0),
		SPKI: memSPKI(t.id),
	}
	last := origin(coreconsensus.JoinRefused, errs.String("why", "no member"))
	q := newJoinQueue(t.net.othersThan(t.id))
	for x, more := q.next(); more; x, more = q.next() {
		pos, code, reason, asked := t.joinOne(ctx, x, &req)
		switch {
		case !asked:
			continue
		case code == 0:
			t.installAnswer(gen, x, pos.Index)
			return pos, nil
		}
		last = joinRefused(code, reason)
		if lead, _, named := leaderOfJoinRefusal(last); named {
			if to := t.net.route(t.id, lead); to != nil {
				// The leader a member that could not answer at once
				// named is tried next, as the TCP transport does (#1422).
				q.follow(to)
			}
		}
	}
	return coreconsensus.PositionValue{}, last
}

// installAnswer installs, in the hold's generation gen, the answer of x to
// a JOIN whose AddLearner is at index at: the JOIN answer carries the
// members the target admits, the target included, and the joiner admits
// them apart from its engine's configuration until it is admitted itself
// (#552), as of the AddLearner it answers (#742).
func (t *MemoryTransport) installAnswer(gen uint64, x *MemoryTransport, at uint64) {
	x.mu.Lock()
	answer := slices.Clone(x.members)
	x.mu.Unlock()
	t.hold.install(gen, func() { t.setMembershipAt(joinGroup, answer, rankedAt(len(answer), at)) })
}

// joinOne sends req to x, under the slot and key the memory transport
// authenticates this node with. asked is false when x serves no JOIN or the
// link to it is cut.
func (t *MemoryTransport) joinOne(ctx context.Context, x *MemoryTransport, req *coreconsensus.JoinRequestValue) (pos coreconsensus.PositionValue, code errs.Code, reason string, asked bool) {
	x.mu.Lock()
	j, jr := x.join, x.joinR
	x.mu.Unlock()
	if (j == nil && jr == nil) || t.net.route(t.id, x.id) == nil {
		return pos, 0, "", false
	}
	peer := coreconsensus.AuthenticatedPeerValue{Slot: req.Slot, SPKI: req.SPKI}
	if jr != nil {
		pos, code, reason = jr(ctx, peer, *req)
		return pos, code, reason, true
	}
	pos, code = j(ctx, peer, *req)
	return pos, code, "", true
}

// othersThan is every transport of the network but self's, in map order.
func (n *MemoryNetwork) othersThan(self coreconsensus.NodeID) []*MemoryTransport {
	n.mu.Lock()
	defer n.mu.Unlock()
	var targets []*MemoryTransport
	for id, x := range n.nodes {
		if id != self {
			targets = append(targets, x)
		}
	}
	return targets
}

// memSPKI is the stand-in key the memory transport reports for a node.
func memSPKI(id coreconsensus.NodeID) [32]byte {
	var k [32]byte
	binary.LittleEndian.PutUint64(k[:], uint64(id))
	k[31] = 1
	return k
}

// MemorySPKI returns the key the memory transport authenticates id with.
func MemorySPKI(id coreconsensus.NodeID) [32]byte { return memSPKI(id) }

// Close stops delivery; a later Bind starts it again, as a restarted node
// does.
func (t *MemoryTransport) Close() error {
	t.mu.Lock()
	r := t.cur
	if r == nil {
		t.mu.Unlock()
		return nil
	}
	t.cur = nil
	r.end()
	t.deliver, t.offer, t.install, t.join, t.joinR, t.membership = nil, nil, nil, nil, nil, nil
	t.mu.Unlock()
	<-r.done
	return nil
}
