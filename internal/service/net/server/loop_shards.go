package server

import (
	"context"
	stdnet "net"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shardsRefusal refuses what Shards cannot mean on a loop group (#856): any
// count but zero — Shards(0) is one listener per loop — and an address whose
// family has no SO_REUSEPORT.
func (g *StreamGroup) shardsRefusal() error {
	//: no Shards given.
	if !g.kinds.has(corenet.OptionShards) {
		//: nothing to refuse.
		return nil
	}
	//: a count the group's loops do not decide.
	if g.limits.Shards != 0 {
		//: refused by name.
		return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("option", corenet.OptionShards.String()),
			errs.Int("shards", g.limits.Shards))
	}
	//: an address the loops cannot share.
	if i := slices.IndexFunc(g.addrs, func(a corenet.AddressValue) bool { return !shardable(a.Network) }); i >= 0 {
		//: refused by name.
		return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", g.name), errs.String("option", corenet.OptionShards.String()),
			errs.String("network", g.addrs[i].Network))
	}
	//: Shards(0) on addresses that can share a port.
	return nil
}

// loopShardCount is how many listeners a loop group opens on addr: one per
// loop for Shards(0), one where the backend is portable (no loop owns a
// listener there) or the operator switched reuseport-loops off (the row reads
// disabled and the group serves one listener).
func loopShardCount(group *StreamGroup, plan loopPlan) int {
	//: nothing to shard, or nothing asked.
	if !group.kinds.has(corenet.OptionShards) || plan.backend == corenet.LoopBackendPortable ||
		slices.Contains(plan.disabled, corenet.CapabilityReuseportLoops) {
		//: one listener, shared.
		return 1
	}
	//: one per loop, the count resolveLanes settled.
	return max(group.loop.Loops, 1)
}

// bindLoopAddress binds one address of a loop group: one listener, or
// Shards(0)'s one per loop sharing the address through SO_REUSEPORT, each
// owned by its loop. An address asked as ":0" draws the port once and the
// other shards join it.
func (s *Server) bindLoopAddress(ctx context.Context, group *StreamGroup, addr corenet.AddressValue, plan loopPlan) ([]stdnet.Listener, []int, error) {
	count := loopShardCount(group, plan)
	share := group.kinds.has(corenet.OptionShareAddress)
	//: an address another process holds is refused before the loops' shards
	//: could join it (#1330).
	if mustProbe(addr, count, share) {
		//: AddressInUse, naming it.
		if err := probeAddress(ctx, addr, true); err != nil {
			//: Start unwinds what is bound.
			return nil, nil, err
		}
	}
	target := addr
	lns := make([]stdnet.Listener, 0, count)
	owners := make([]int, 0, count)
	//: one listener a loop, or the one the loops share.
	for i := range count {
		ln, rep, err := listen(ctx, target, nil, socketSpec{
			reuse: reuseOf(count, share), attempts: s.rebindAttempts(target), backlog: group.limits.Backlog,
			receiveBuffer: group.limits.ReceiveBuffer, sendBuffer: group.limits.SendBuffer,
			keepAlive: s.keepAliveOf(group), userTimeout: s.userTimeoutOf(group), plainTCP: plainTCPListens(group, plan),
		})
		//: a shard that will not bind aborts the start, never a smaller group.
		if err != nil {
			//: Start unwinds what is bound.
			return nil, nil, err
		}
		shards := 0
		//: the first shard names the address's one row, and the port.
		if i == 0 {
			shards = count
			target = pinnedAddr(addr, ln)
		}
		s.registerLoopListeners(group, []stdnet.Listener{ln}, &rep, shards)
		lns = append(lns, group.loopLayers([]stdnet.Listener{ln})...)
		owner := 0
		//: a shard is its loop's alone; one listener is everybody's.
		if count > 1 {
			owner = i + 1
		}
		owners = append(owners, owner)
	}
	//: serving nothing yet.
	return lns, owners, nil
}

// pinnedAddr is addr with the port ln drew, so the shards of an address asked
// as ":0" join it instead of drawing their own.
func pinnedAddr(addr corenet.AddressValue, ln stdnet.Listener) corenet.AddressValue {
	//: the listener's port.
	return withBoundPort(addr, ln.Addr())
}
