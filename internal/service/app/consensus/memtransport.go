package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"maps"
	"slices"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
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

// MemoryTransport is one node's end of a MemoryNetwork.
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
	members []coreconsensus.MemberValue
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
	dst := t.net.route(t.id, to)
	if dst == nil {
		return
	}
	q := dst.data
	if lane, err := wire.Lane(frame); err == nil && lane == coreconsensus.LaneControl {
		q = dst.ctrl
	}
	select {
	case q <- memFrame{from: t.id, frame: bytes.Clone(frame)}:
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
	dst.mu.Lock()
	of, in := dst.offer, dst.install
	dst.mu.Unlock()
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
	ierr := in(coreconsensus.InstallParams{Ctx: ctx, From: peer, Offer: offer, Offset: acc.Offset, Body: rc})
	// The install's verdict wins; a Close failure of the stream it read is
	// reported only when the install itself succeeded.
	if cerr := rc.Close(); cerr != nil && ierr == nil {
		return cerr
	}
	return ierr
}

// Bind implements Transport.
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
		t.mu.Lock()
		d := t.deliver
		t.mu.Unlock()
		if d != nil {
			d(coreconsensus.AuthenticatedPeerValue{ID: f.from}, f.frame)
		}
	}
}

// BindJoinReason implements JoinReasonBinder.
func (t *MemoryTransport) BindJoinReason(_ coreconsensus.GroupID, join func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)) {
	t.mu.Lock()
	t.joinR = join
	t.mu.Unlock()
}

// SetMembership implements Transport.
func (t *MemoryTransport) SetMembership(_ coreconsensus.GroupID, admitted []coreconsensus.MemberValue) {
	t.mu.Lock()
	t.members = append(t.members[:0], admitted...)
	t.mu.Unlock()
}

// Join implements Joiner over the network, as the TCP transport does.
func (t *MemoryTransport) Join(ctx context.Context, cfg coreconsensus.JoinConfig) (coreconsensus.PositionValue, error) {
	targets := t.net.othersThan(t.id)
	req := coreconsensus.JoinRequestValue{
		Cluster: cfg.Stamp.Log.Cluster, ID: cfg.Stamp.ID, Incarnation: cfg.Stamp.Incarnation,
		Slot: cfg.Stamp.Slot, Address: cfg.Address,
	}
	last := origin(coreconsensus.JoinRefused, errs.String("why", "no member"))
	for _, x := range targets {
		x.mu.Lock()
		j, jr := x.join, x.joinR
		x.mu.Unlock()
		if (j == nil && jr == nil) || t.net.route(t.id, x.id) == nil {
			continue
		}
		peer := coreconsensus.AuthenticatedPeerValue{Slot: cfg.Stamp.Slot, SPKI: memSPKI(t.id)}
		req.SPKI = peer.SPKI
		var pos coreconsensus.PositionValue
		var code errs.Code
		var reason string
		if jr != nil {
			pos, code, reason = jr(ctx, peer, req)
		} else {
			pos, code = j(ctx, peer, req)
		}
		if code == 0 {
			return pos, nil
		}
		last = joinRefused(code, reason)
	}
	return coreconsensus.PositionValue{}, last
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
	t.deliver, t.offer, t.install, t.join, t.joinR = nil, nil, nil, nil, nil
	t.mu.Unlock()
	<-r.done
	return nil
}
