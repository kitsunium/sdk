package consensus

import (
	"bytes"
	"context"
	"io"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

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

// Transport returns the transport of id, creating it.
func (n *MemoryNetwork) Transport(id coreconsensus.NodeID) *MemoryTransport {
	n.mu.Lock()
	defer n.mu.Unlock()
	if t, ok := n.nodes[id]; ok {
		return t
	}
	t := &MemoryTransport{
		net: n, id: id,
		ctrl: make(chan memFrame, 4096), data: make(chan memFrame, 4096),
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
	ids := make([]coreconsensus.NodeID, 0, len(n.nodes))
	for x := range n.nodes {
		ids = append(ids, x)
	}
	n.mu.Unlock()
	for _, x := range ids {
		if x != id {
			n.Cut(id, x, cut)
			n.Cut(x, id, cut)
		}
	}
}

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
	deliver func(coreconsensus.AuthenticatedPeer, []byte)
	offer   func(coreconsensus.AuthenticatedPeer, coreconsensus.InstallOffer) coreconsensus.InstallAccept
	install func(coreconsensus.InstallRequest) error
	join    func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code)
	joinR   func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code, string)
	ctrl    chan memFrame
	data    chan memFrame
	cur     *memRun
	members []coreconsensus.Member
}

// memRun is one delivery goroutine's life: Close closes stop, once, and
// waits on done, which the goroutine closes as it returns. Each Bind after a
// Close starts a run of its own, so a Close never waits on — or stops — a
// run a later Bind started.
type memRun struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
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
func (t *MemoryTransport) Stream(ctx context.Context, to coreconsensus.NodeID, offer coreconsensus.InstallOffer, open func(int64) (io.ReadCloser, error)) error {
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
	peer := coreconsensus.AuthenticatedPeer{ID: t.id}
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
	ierr := in(coreconsensus.InstallRequest{Ctx: ctx, From: peer, Offer: offer, Offset: acc.Offset, Body: rc})
	// The install's verdict wins; a Close failure of the stream it read is
	// reported only when the install itself succeeded.
	if cerr := rc.Close(); cerr != nil && ierr == nil {
		return cerr
	}
	return ierr
}

// Bind implements Transport.
func (t *MemoryTransport) Bind(group coreconsensus.GroupID,
	deliver func(coreconsensus.AuthenticatedPeer, []byte),
	offer func(coreconsensus.AuthenticatedPeer, coreconsensus.InstallOffer) coreconsensus.InstallAccept,
	install func(coreconsensus.InstallRequest) error,
	join func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code),
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
	defer close(r.done)
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
			d(coreconsensus.AuthenticatedPeer{ID: f.from}, f.frame)
		}
	}
}

// BindJoinReason implements JoinReasonBinder.
func (t *MemoryTransport) BindJoinReason(_ coreconsensus.GroupID, join func(context.Context, coreconsensus.AuthenticatedPeer, coreconsensus.JoinRequest) (coreconsensus.Position, errs.Code, string)) {
	t.mu.Lock()
	t.joinR = join
	t.mu.Unlock()
}

// SetMembership implements Transport.
func (t *MemoryTransport) SetMembership(_ coreconsensus.GroupID, admitted []coreconsensus.Member) {
	t.mu.Lock()
	t.members = append(t.members[:0], admitted...)
	t.mu.Unlock()
}

// Join implements Joiner over the network, as the TCP transport does.
func (t *MemoryTransport) Join(ctx context.Context, cfg coreconsensus.JoinConfig) (coreconsensus.Position, error) {
	t.net.mu.Lock()
	var targets []*MemoryTransport
	for id, x := range t.net.nodes {
		if id != t.id {
			targets = append(targets, x)
		}
	}
	t.net.mu.Unlock()
	req := coreconsensus.JoinRequest{
		Cluster: cfg.Stamp.Log.Cluster, ID: cfg.Stamp.ID, Incarnation: cfg.Stamp.Incarnation,
		Slot: cfg.Stamp.Slot, Address: cfg.Address,
	}
	var last error = origin(coreconsensus.JoinRefused, errs.String("why", "no member"))
	for _, x := range targets {
		x.mu.Lock()
		j, jr := x.join, x.joinR
		x.mu.Unlock()
		if (j == nil && jr == nil) || t.net.route(t.id, x.id) == nil {
			continue
		}
		peer := coreconsensus.AuthenticatedPeer{Slot: cfg.Stamp.Slot, SPKI: memSPKI(t.id)}
		req.SPKI = peer.SPKI
		var pos coreconsensus.Position
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
	return coreconsensus.Position{}, last
}

// memSPKI is the stand-in key the memory transport reports for a node.
func memSPKI(id coreconsensus.NodeID) [32]byte {
	var k [32]byte
	for i := 0; i < 8; i++ {
		k[i] = byte(uint64(id) >> (8 * i))
	}
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
