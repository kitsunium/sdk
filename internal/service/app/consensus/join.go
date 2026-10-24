package consensus

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// joinRefreshTimeout bounds one JOIN sent again, every address included
// (#632).
const joinRefreshTimeout time.Duration = 30 * time.Second

// ---- pre-admission JOIN (D4, R8-09) ----

// join is the JOIN handler a transport without reasons binds: joinReason's
// verdict without its words.
func (n *Node) join(ctx context.Context, from coreconsensus.AuthenticatedPeerValue, r coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code) {
	pos, code, _ := n.joinReason(ctx, from, r)
	return pos, code
}

// joinReason answers a JOIN and, when it refuses, says why: the
// authorizer's own words, sanitized and bounded, or the engine's rule. An
// authorizer that says nothing leaves the reason empty.
func (n *Node) joinReason(ctx context.Context, from coreconsensus.AuthenticatedPeerValue, r coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string) {
	refuse := func(why string) (coreconsensus.PositionValue, errs.Code, string) {
		return coreconsensus.PositionValue{}, coreconsensus.CodeJoinRefused, why
	}
	if why := n.screenJoin(&from, &r); why != "" {
		return refuse(why)
	}
	r.SPKI = from.SPKI
	if pos, ok := n.alreadyJoined(&r); ok {
		// The member itself asks again: a joiner restarted before anything
		// reached its log, or one whose answer was lost. It is answered with
		// the members, which its transport admits only from a JOIN answer,
		// and nothing is proposed, nor counted against the slot (#533).
		return pos, 0, ""
	}
	if n.joinRateLimited(r.Slot) {
		return refuse("one JOIN per slot per election timeout")
	}
	if n.slotTaken(r.Slot) {
		// A slot that still has a live member is refused (R8-11).
		return refuse("the slot still has a member")
	}
	op, err := n.cfg.JoinAuthorizer.Authorize(ctx, r)
	if err != nil {
		n.obs.warn("consensus: JOIN refused by the authorizer", lstr("slot", r.Slot), lstr("error", err.Error()))
		return refuse(joinReasonOf(err))
	}
	if !op.Valid() {
		return refuse("the authorizer returned an invalid operator")
	}
	return n.addJoiner(ctx, op, &r)
}

// screenJoin is the engine's own refusal of a JOIN, before the rate limit
// or the membership is consulted: no authorizer, another cluster, a slot that is not the one the
// certificate names, or a peer that proved no key. It returns the reason,
// or "" when the JOIN goes on.
func (n *Node) screenJoin(from *coreconsensus.AuthenticatedPeerValue, r *coreconsensus.JoinRequestValue) string {
	var noKey [32]byte
	switch {
	case n.cfg.JoinAuthorizer == nil:
		return "this cluster admits no JOIN"
	case r.Cluster != n.lineage().Cluster:
		return "another cluster"
	case r.Slot != from.Slot:
		return "the slot differs from the certificate's"
	case from.SPKI == noKey:
		return "no key"
	}
	return ""
}

// joinRateLimited admits one JOIN per slot per minimum election timeout
// (R7-42) and reports whether this one is over that rate, counting it.
// An admitted JOIN starts the slot's window even when it is refused later.
func (n *Node) joinRateLimited(slot string) bool {
	n.joinMu.Lock()
	if n.lastJoin == nil {
		n.lastJoin = map[string]time.Time{}
	}
	now := n.clk.Now()
	if last, ok := n.lastJoin[slot]; ok && now.Sub(last) < n.Status().Timing.ElectionTimeoutMin {
		n.joinMu.Unlock()
		n.joinRefused.Add(1)
		n.obs.inc(mJoinsRateLimited)
		return true
	}
	n.lastJoin[slot] = now
	n.joinMu.Unlock()
	return false
}

// alreadyJoined reports whether the membership in force lists the
// requester itself — its id, incarnation, slot and key all equal — and
// answers with the position of that membership. Any other request on a held
// slot is slotTaken's to refuse (R8-11): the guard is against another node
// taking it, never against the member asking again.
func (n *Node) alreadyJoined(r *coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, bool) {
	var pos coreconsensus.PositionValue
	found := false
	n.exec(func() {
		conf := n.r.Status().Conf
		m, ok := conf.Member(r.ID)
		if !ok || m.Incarnation != r.Incarnation || m.Slot != r.Slot || m.SPKI != r.SPKI {
			return
		}
		term, _ := n.r.TermAt(conf.Index)
		pos, found = coreconsensus.PositionValue{Term: term, Index: conf.Index}, true
	})
	return pos, found
}

// slotTaken reports whether a current member already holds slot.
func (n *Node) slotTaken(slot string) bool {
	for _, m := range n.Membership().Members {
		if m.Slot == slot {
			return true
		}
	}
	return false
}

// addJoiner proposes the authorized joiner as a learner, under the
// operator the authorizer named, and answers with the entry's position or
// the code and reason of its failure.
func (n *Node) addJoiner(ctx context.Context, op coreconsensus.Operator, r *coreconsensus.JoinRequestValue) (coreconsensus.PositionValue, errs.Code, string) {
	// The limit the joiner announced travels with it: every member's cut
	// is held to the smallest (R2-30, #556). 0, a joiner that announced
	// nothing, leaves the cuts at the members' own limits.
	m := coreconsensus.MemberValue{
		ID: r.ID, Incarnation: r.Incarnation, Slot: r.Slot, SPKI: r.SPKI, Address: r.Address, Joining: true,
		MaxSnapshotBytes: max(r.MaxSnapshotBytes, 0),
	}
	a := new(wire.AdminValue{
		Operator: string(op), Change: wire.ChangeAddLearner, Target: m.ID,
		Membership: coreconsensus.MembershipValue{Members: []coreconsensus.MemberValue{m}},
	})
	data := wire.AppendAdmin(nil, a)
	tok, _, err := n.submit(ctx, coreconsensus.EntryValue{Kind: coreconsensus.EntryMembership, BodySum: wire.BodySum(data), Data: data}, true)
	if err != nil {
		c, ok := errs.CodeOf(err)
		if !ok {
			c = coreconsensus.CodeJoinRefused
		}
		reason, _ := errs.ReasonOf(err)
		return coreconsensus.PositionValue{}, c, reason
	}
	return tok.Position(), 0, ""
}

// ---- a joining node's JOIN sent again (#632) ----

// joinHold is a transport's hold on its JOIN answer: the configuration it
// last joined with, kept so that a joining node that hears no leader can
// send its JOIN again (#632), and whether the node's admission released the
// answer. A released answer is installed again by a new Join only, never by
// a JOIN sent again: the configuration admits from then on (#552).
type joinHold struct {
	mu       sync.Mutex
	cfg      *coreconsensus.JoinConfig
	released bool
}

// begin opens an explicit Join: its answer is installed even after a
// release.
func (h *joinHold) begin() {
	h.mu.Lock()
	h.released = false
	h.mu.Unlock()
}

// keep records the configuration of a Join that succeeded.
func (h *joinHold) keep(cfg *coreconsensus.JoinConfig) {
	c := *cfg
	c.Addresses, c.Pins = slices.Clone(cfg.Addresses), slices.Clone(cfg.Pins)
	h.mu.Lock()
	h.cfg = &c
	h.mu.Unlock()
}

// again is the configuration to send the JOIN again with; ok is false
// when no Join succeeded or the answer was released.
func (h *joinHold) again() (cfg coreconsensus.JoinConfig, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil || h.released {
		return coreconsensus.JoinConfig{}, false
	}
	return *h.cfg, true
}

// install runs set, which installs an answer, unless the answer was
// released: a JOIN sent again that is answered after the node's admission
// would otherwise keep admitting members its configuration no longer lists.
func (h *joinHold) install(set func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.released {
		set()
	}
}

// release runs drop, which drops the answer, and keeps any later JOIN sent
// again from installing one.
func (h *joinHold) release(drop func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.released = true
	drop()
}

// sameAnswer reports whether two JOIN answers admit the same members under
// the same keys.
func sameAnswer(a, b []coreconsensus.MemberValue) bool {
	byID := func(x, y coreconsensus.MemberValue) int { return cmp.Compare(x.ID, y.ID) }
	return slices.EqualFunc(slices.SortedFunc(slices.Values(a), byID), slices.SortedFunc(slices.Values(b), byID),
		func(x, y coreconsensus.MemberValue) bool { return x.ID == y.ID && x.SPKI == y.SPKI })
}

// refreshJoiner is a transport that sends its JOIN again with the
// configuration it last joined with, replacing a held answer, and reports
// whether the answer changed (#632).
type refreshJoiner interface {
	refreshJoin(ctx context.Context) (changed bool, err error)
}

// refreshJoin sends this node's JOIN again, off the event loop, when raft
// reports it heard no leader while the node is still joining (#632). Its
// transport admits, apart from the engine's configuration, only the members
// its JOIN answer listed: a leader admitted after that answer was refused at
// every connection, and a learner, which never campaigns, had no other way
// to learn of it, so the joiner was never fed while that leader led. A
// member answers a node its configuration already lists without proposing
// anything or counting it against the slot (#533), and the fresh answer
// replaces the held one.
//
// The JOIN runs on a goroutine counted in n.wg, one at a time, which ends
// once the JOIN is answered or refused, at joinRefreshTimeout, or once the
// node stops, which cancels it.
func (n *Node) refreshJoin(rd *raft.ReadyValue) {
	j, ok := n.cfg.Transport.(refreshJoiner)
	if !rd.LeaderUnheard || n.canVote || !ok || !n.rejoining.CompareAndSwap(false, true) {
		return
	}
	n.wg.Add(1)
	go n.runJoinRefresh(j)
}

// runJoinRefresh is refreshJoin's goroutine.
func (n *Node) runJoinRefresh(j refreshJoiner) {
	defer n.wg.Done()
	defer n.rejoining.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), joinRefreshTimeout)
	defer cancel()
	go func() {
		select {
		case <-n.stopc:
			cancel()
		case <-ctx.Done():
		}
	}()
	if changed, err := j.refreshJoin(ctx); err == nil && changed {
		n.obs.info("consensus: no leader heard while joining; the JOIN sent again admits a new answer")
	}
}
