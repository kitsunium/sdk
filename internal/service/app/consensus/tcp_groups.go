package consensus

import (
	"context"
	"encoding/binary"
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ---- several groups on one transport (ADR 0244, #925) ----
//
// A TCPTransport serves every group bound to it on one listener: a frame
// goes to the group its frozen header names (bytes 6 to 13, ADR 0152 D18),
// a snapshot offer to the group its lane names, and a frame or an offer of
// a group nothing bound is dropped and counted. Bind used to keep one set
// of callbacks, so a second Node on the transport silently replaced the
// first's and was handed every frame of both groups.

const (
	// laneSnapshotGroup carries one snapshot offer and its stream for the
	// group its offer names: the group as a little-endian u64, then
	// laneSnapshot's offer, in one chunk. Group 1 keeps laneSnapshot, so a
	// cluster of one group 1 sends what it always did. 5 and 6 are left to
	// ADR 0220's laneControlV2 and laneDataV2.
	laneSnapshotGroup byte = 7
	// groupPrefix is the group's bytes before laneSnapshot's offer on
	// laneSnapshotGroup.
	groupPrefix int = 8
	// frameGroupAt is the offset of the group in a frame as readFrames
	// reads it, its length included: length (u32), version (u8), type
	// (u8), then the group (u64), little-endian (ADR 0152 D18).
	frameGroupAt int = 4 + 1 + 1
	// defaultGroup is the group a lane that names none is served to when
	// several are bound: the one every v1 node runs (Config clamps 0 to it).
	defaultGroup coreconsensus.GroupID = 1
)

// binding is one group's callbacks: those Bind registers, the JOIN with
// its reasons, and the two hooks a node binds beside them under its group
// (groupBinder).
type binding struct {
	deliver     func(coreconsensus.AuthenticatedPeerValue, []byte)
	offer       func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue
	install     func(coreconsensus.InstallParams) error
	join        func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code)
	joinR       func(context.Context, coreconsensus.AuthenticatedPeerValue, coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string)
	unreachable func(coreconsensus.NodeID)
	undecodable func(coreconsensus.AuthenticatedPeerValue, uint64, error) errs.Code
	// membership answers laneMembership's read of the configuration the
	// group runs with (#763), when the group is the one that lane is
	// served to (legacyBinding).
	membership func() (coreconsensus.MembershipValue, bool)
}

// groupBinder is the sibling of a transport that serves several groups: the
// hooks a node binds beside Bind, which name no group of their own, bound
// under the node's. Bound through UnreachableReporter and undecodableBinder
// instead, the second node on a transport replaced the first's (#925).
type groupBinder interface {
	unreachableFor(group coreconsensus.GroupID, f func(coreconsensus.NodeID))
	undecodableFor(group coreconsensus.GroupID, judge func(coreconsensus.AuthenticatedPeerValue, uint64, error) errs.Code)
	membershipFor(group coreconsensus.GroupID, read func() (coreconsensus.MembershipValue, bool))
}

// groupStreamer is the sibling of a transport whose snapshot offer names the
// group it installs in; Transport.Stream names none.
type groupStreamer interface {
	streamGroup(ctx context.Context, group coreconsensus.GroupID, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error
}

// streamOffer streams offer to to for group: group 1's, and any group's on
// a transport that is no groupStreamer, through Stream, so that a transport
// wrapping a TCPTransport and its Stream still carries a v1 cluster's
// snapshots; another group's on a groupStreamer under that group.
func streamOffer(ctx context.Context, tr coreconsensus.Transport, group coreconsensus.GroupID, to coreconsensus.NodeID, offer coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	if gs, ok := tr.(groupStreamer); ok && group != defaultGroup {
		return gs.streamGroup(ctx, group, to, offer, open)
	}
	return tr.Stream(ctx, to, offer, open)
}

// bindingOf is group's binding, created on its first callback; under mu.
func (t *TCPTransport) bindingOf(group coreconsensus.GroupID) *binding {
	b, ok := t.groups[group]
	if !ok {
		b = &binding{}
		t.groups[group] = b
	}
	return b
}

// legacyBinding is the binding a lane that names no group is served to —
// laneSnapshot, laneJoin, laneJoinV2 and laneMembership: the only group
// bound, or with several the default group, and none when it is not bound;
// under mu. A node of one group, whatever its number, is served as it
// always was.
func (t *TCPTransport) legacyBinding() *binding {
	if len(t.groups) == 1 {
		for _, b := range t.groups {
			return b
		}
	}
	return t.groups[defaultGroup]
}

// frameGroup is the group a frame read by readFrames names; readFrame
// refuses one shorter than its header.
func frameGroup(frame []byte) coreconsensus.GroupID {
	return coreconsensus.GroupID(binary.LittleEndian.Uint64(frame[frameGroupAt:]))
}

// unroutedGroup counts a frame or a snapshot offer from a member naming a
// group nothing bound on this transport, which is dropped, and logs it at
// the first and at each power of two after it: a peer's group this node
// does not run retransmits at every heartbeat.
func (t *TCPTransport) unroutedGroup(from coreconsensus.NodeID, group coreconsensus.GroupID, lane byte) {
	n := t.unrouted.Add(1)
	if n&(n-1) != 0 {
		return
	}
	t.mu.Lock()
	warn := t.warn
	t.mu.Unlock()
	if warn != nil {
		warn("consensus: a frame of a group this transport does not serve was dropped", lattr("from", uint64(from)),
			lattr("group", uint64(group)), lattr("lane", uint64(lane)), lattr("dropped", uint64(n)))
	}
}

// unreachableFor implements groupBinder.
func (t *TCPTransport) unreachableFor(group coreconsensus.GroupID, f func(coreconsensus.NodeID)) {
	t.mu.Lock()
	t.bindingOf(group).unreachable = f
	t.mu.Unlock()
}

// undecodableFor implements groupBinder.
func (t *TCPTransport) undecodableFor(group coreconsensus.GroupID, judge func(coreconsensus.AuthenticatedPeerValue, uint64, error) errs.Code) {
	t.mu.Lock()
	t.bindingOf(group).undecodable = judge
	t.mu.Unlock()
}

// membershipFor implements groupBinder: read answers laneMembership for
// group, bound beside bindMembership's, which names none (#763, #925).
func (t *TCPTransport) membershipFor(group coreconsensus.GroupID, read func() (coreconsensus.MembershipValue, bool)) {
	t.mu.Lock()
	t.bindingOf(group).membership = read
	t.mu.Unlock()
}

// unreachableHooks is every callback an unreachable peer is reported to:
// the transport's own (Unreachable), then each group's; under mu.
func (t *TCPTransport) unreachableHooks() []func(coreconsensus.NodeID) {
	var fs []func(coreconsensus.NodeID)
	if t.unreachable != nil {
		fs = append(fs, t.unreachable)
	}
	for _, b := range t.groups {
		if b.unreachable != nil {
			fs = append(fs, b.unreachable)
		}
	}
	return fs
}

// groupOffer is the wire form of an offer on laneSnapshotGroup: the group,
// then body, encodeOffer's.
func groupOffer(group coreconsensus.GroupID, body []byte) []byte {
	return append(binary.LittleEndian.AppendUint64(make([]byte, 0, groupPrefix+len(body)), uint64(group)), body...)
}

// offerBinding reads the offer chunk of a snapshot connection on lane and
// the binding it is served to: laneSnapshot's to legacyBinding, and
// laneSnapshotGroup's to the group it names, whose prefix is cut off the
// chunk returned. group is the one named, 0 on laneSnapshot; bound is nil
// when that group is not bound.
func (t *TCPTransport) offerBinding(r io.Reader, lane byte) (chunk []byte, group coreconsensus.GroupID, bound *binding, err error) {
	limit := t.cfg.MaxFrameBytes
	if lane == laneSnapshotGroup {
		limit += groupPrefix
	}
	chunk, err = readChunk(r, limit)
	if err != nil {
		return nil, 0, nil, err
	}
	if lane == laneSnapshotGroup {
		if len(chunk) < groupPrefix {
			return nil, 0, nil, origin(coreconsensus.FrameInvalid, errs.String("what", "snapshot offer group"))
		}
		group = coreconsensus.GroupID(binary.LittleEndian.Uint64(chunk))
		chunk = chunk[groupPrefix:]
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if lane == laneSnapshotGroup {
		return chunk, group, t.groups[group], nil
	}
	return chunk, 0, t.legacyBinding(), nil
}
