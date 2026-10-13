package consensus

import (
	"context"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

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
	case r.Cluster != n.ident.Cluster:
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
	m := coreconsensus.MemberValue{ID: r.ID, Incarnation: r.Incarnation, Slot: r.Slot, SPKI: r.SPKI, Address: r.Address, Joining: true}
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
