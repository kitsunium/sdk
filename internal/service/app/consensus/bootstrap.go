package consensus

import (
	"context"
	"crypto/sha256"
	"slices"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// foundingDigest is the digest of the founding member list, so that a rerun
// with another list is refused.
func foundingDigest(members []coreconsensus.MemberValue) [32]byte {
	return sha256.Sum256(wire.AppendMembership(nil, &coreconsensus.MembershipValue{Members: byID(members)}))
}

// byID is the founding list in ID order: the digest AND entry 1 are built
// from it, so founders given the same members in any order write the same
// entry (review M1).
func byID(members []coreconsensus.MemberValue) []coreconsensus.MemberValue {
	ms := slices.Clone(members)
	slices.SortFunc(ms, func(a, b coreconsensus.MemberValue) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return ms
}

// Bootstrap founds a cluster on one founding node's log, in phases that are
// each idempotent: a bootstrap-pending stamp naming the founding list, the
// founding configuration as entry 1, then the member stamp. Every founder
// runs it with the IDENTICAL list (D8, R2-24, R3-02, R9-04). It never runs
// inside NewNode.
func Bootstrap(ctx context.Context, cfg *Config, members []coreconsensus.MemberValue) error {
	// Every phase below is a durable write: a caller that gave up before the
	// first one gets its own cancellation back, and nothing is written.
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.Log == nil {
		return misconfigured("Log", "nil")
	}
	if err := checkFounders(members, cfg.MinVoters); err != nil {
		return err
	}
	timing, err := foundingTiming(cfg)
	if err != nil {
		return err
	}
	stamp, err := founderStamp(cfg)
	if err != nil {
		return err
	}
	if err := checkSelfListed(members, &stamp); err != nil {
		return err
	}
	digest := foundingDigest(members)
	if done, err := advanceFounding(cfg.Log, &stamp, digest); done || err != nil {
		return err
	}
	if err := writeFoundingEntry(cfg, members, timing); err != nil {
		return err
	}
	stamp.State = coreconsensus.StampMember
	if err := cfg.Log.WriteStamp(stamp); err != nil {
		return storageFailed("stamp", err)
	}
	return nil
}

// checkFounders refuses a founding list no cluster may start from: empty,
// with a zero or repeated id, with a member that carries no SPKI pin, with
// two members pinned to one key, or with fewer voters than MinVoters. It reads nothing from disk, so a bad
// list is refused before the stamp is touched.
func checkFounders(members []coreconsensus.MemberValue, minVoters int) error {
	if len(members) == 0 {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "no member"))
	}
	var noPin [32]byte
	seen := map[coreconsensus.NodeID]bool{}
	for _, m := range members {
		if m.ID == 0 || seen[m.ID] {
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a zero or repeated id"))
		}
		if m.SPKI == noPin {
			// An empty pin is refused: no member is admitted on its slot
			// alone (R9-04).
			return origin(coreconsensus.BootstrapRefused, errs.String("why", "a member without an SPKI pin"))
		}
		seen[m.ID] = true
	}
	if sharedKey(members) {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "two members under one key"))
	}
	if minVoters > countVoters(members) {
		return misconfigured("MinVoters", "above the founding voter count")
	}
	return nil
}

// countVoters is how many members of a list vote.
func countVoters(members []coreconsensus.MemberValue) int {
	voters := 0
	for _, m := range members {
		if m.Voter {
			voters++
		}
	}
	return voters
}

// founderStamp reads the stamp Bootstrap advances, refusing a log that was
// never initialised or that belongs to another cluster or node.
func founderStamp(cfg *Config) (coreconsensus.StampValue, error) {
	stamp, err := cfg.Log.Stamp()
	if err != nil {
		return stamp, storageFailed("stamp", err)
	}
	if stamp.State == 0 {
		return stamp, origin(coreconsensus.BootstrapRefused, errs.String("why", "unstamped: Init first"))
	}
	if stamp.Log.Cluster != cfg.Cluster || (cfg.ID != 0 && cfg.ID != stamp.ID) {
		return stamp, origin(coreconsensus.DataDirMismatch, errs.String("why", "cluster or node id"))
	}
	return stamp, nil
}

// checkSelfListed refuses a founding list that does not name this node with
// the incarnation and slot it was stamped with: every founder must run
// Bootstrap with the same list, and that list must include itself.
func checkSelfListed(members []coreconsensus.MemberValue, stamp *coreconsensus.StampValue) error {
	self := slices.IndexFunc(members, func(m coreconsensus.MemberValue) bool { return m.ID == stamp.ID })
	if self < 0 || members[self].Incarnation != stamp.Incarnation || members[self].Slot != stamp.Slot {
		return origin(coreconsensus.BootstrapRefused, errs.String("why", "this node is not in the list as stamped"))
	}
	return nil
}

// advanceFounding runs the stamp phase of Bootstrap. It reports done when
// the node is already a member founded with this list, refuses a list that
// differs from the one already recorded, and moves an empty joining log to
// bootstrap-pending with the list's digest. A stamp state it does not
// define is refused, never founded.
func advanceFounding(log coreconsensus.LogStore, stamp *coreconsensus.StampValue, digest [32]byte) (bool, error) {
	switch stamp.State {
	case coreconsensus.StampMember:
		if stamp.Founding == digest {
			return true, nil // already founded with this list
		}
		return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "already founded with another list"))
	case coreconsensus.StampBootstrapPending:
		if stamp.Founding != digest {
			return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "bootstrap pending with another list"))
		}
	case coreconsensus.StampJoining:
		if first, last := log.Bounds(); last >= first {
			return false, origin(coreconsensus.BootstrapRefused, errs.String("why", "the log already holds entries"))
		}
		stamp.State, stamp.Founding = coreconsensus.StampBootstrapPending, digest
		if err := log.WriteStamp(*stamp); err != nil {
			return false, storageFailed("stamp", err)
		}
	default:
		// State 0 was refused by founderStamp; any other value is one this
		// version does not define, and a directory it cannot read the state
		// of is never founded (a LogStore other than the shipped one may
		// hand back any byte).
		return false, origin(coreconsensus.DataDirMismatch, errs.String("why", "unknown stamp state"))
	}
	return false, nil
}

// foundingTiming is the generation 1 contract the founding entry carries:
// cfg's InitialTiming completed field by field and refused as NewNode
// completes and refuses it, so the contract that rules once entry 1 commits
// is exactly the one NewNode validated. Written raw, a timing with only its
// drift set became TimingDefaults with no drift, one without its maximum
// refused every promotion, one without its minimum was refused by the
// engine and left each node on its own InitialTiming (#523).
func foundingTiming(cfg *Config) (coreconsensus.TimingContractSpec, error) {
	c := *cfg
	clampTiming(&c.InitialTiming)
	if err := refuseTiming(&c); err != nil {
		return coreconsensus.TimingContractSpec{}, err
	}
	timing := c.InitialTiming
	timing.Generation = 1
	return timing, nil
}

// writeFoundingEntry writes the founding configuration as entry 1, at term
// 1 and committed, with the generation 1 contract timing, then syncs it; a
// log that already holds an entry is left alone, which is what makes a
// rerun idempotent.
func writeFoundingEntry(cfg *Config, members []coreconsensus.MemberValue, timing coreconsensus.TimingContractSpec) error {
	if first, last := cfg.Log.Bounds(); last >= first {
		return nil
	}
	conf := coreconsensus.MembershipValue{Members: byID(members)}
	for i := range conf.Members {
		conf.Members[i].Joining = false
	}
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: "bootstrap", Change: wire.ChangeFounding, Membership: conf, Contract: timing})
	e := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: 1, Index: 1}, Kind: coreconsensus.EntryMembership,
		BodySum: wire.BodySum(data), Data: data,
	}
	e.ChainSum = wire.ChainSum(0, &e)
	hs := new(coreconsensus.HardState{Term: 1, Commit: 1})
	if err := cfg.Log.Append([]coreconsensus.EntryValue{e}, hs); err != nil {
		return storageFailed("append", err)
	}
	if err := cfg.Log.Sync(); err != nil {
		return storageFailed("sync", err)
	}
	return nil
}

// sharedKey reports whether two members are pinned to one key. The
// transport admits a connection by its key: two members under one key
// could speak as each other (review M5).
func sharedKey(members []coreconsensus.MemberValue) bool {
	var noPin [32]byte
	seen := make(map[[32]byte]bool, len(members))
	for _, m := range members {
		if m.SPKI == noPin {
			continue
		}
		if seen[m.SPKI] {
			return true
		}
		seen[m.SPKI] = true
	}
	return false
}
