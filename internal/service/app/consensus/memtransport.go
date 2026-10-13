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
		ctrl: make(chan memFrame, 4096), data: make(chan memFrame, 4096), stop: make(chan struct{}),
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
	ctrl    chan memFrame
	data    chan memFrame
	stop    chan struct{}
	bound   bool
	members []coreconsensus.Member
	wg      sync.WaitGroup
}

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
	defer rc.Close()
	return in(coreconsensus.InstallRequest{Ctx: ctx, From: peer, Offer: offer, Offset: acc.Offset, Body: rc})
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
	if !t.bound {
		t.bound = true
		t.wg.Add(1)
		go t.run(t.stop)
	}
	return nil
}

func (t *MemoryTransport) run(stop chan struct{}) {
	defer t.wg.Done()
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
		j := x.join
		x.mu.Unlock()
		if j == nil || t.net.route(t.id, x.id) == nil {
			continue
		}
		peer := coreconsensus.AuthenticatedPeer{Slot: cfg.Stamp.Slot, SPKI: memSPKI(t.id)}
		req.SPKI = peer.SPKI
		pos, code := j(ctx, peer, req)
		if code == 0 {
			return pos, nil
		}
		last = origin(sentinelOf(code))
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
	if !t.bound {
		t.mu.Unlock()
		return nil
	}
	t.bound = false
	close(t.stop)
	t.deliver, t.offer, t.install, t.join = nil, nil, nil, nil
	t.mu.Unlock()
	t.wg.Wait()
	t.mu.Lock()
	t.stop = make(chan struct{})
	t.mu.Unlock()
	return nil
}
