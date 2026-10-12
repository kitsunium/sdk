package consensus

import (
	"crypto/ed25519"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The offline functions work on the shipped layout, and only on it (D17,
// R2-33), on a stopped node: each refuses a DataDir a live Node holds.

// Init prepares a joining node purely offline: a random NodeID, a joining
// stamp, and the pair the operator or the JOIN gives to AddLearner. It is
// idempotent (R3-02, R7-41, R8-10).
func Init(d DataDir, cluster coreconsensus.ClusterID, slot string) (coreconsensus.NodeID, coreconsensus.Incarnation, error) {
	if err := d.offline(); err != nil {
		return 0, coreconsensus.Incarnation{}, err
	}
	return InitLog(d.Log(), cluster, slot)
}

// Redraw draws a new id, only while the stamp is joining and the log empty,
// after a JOIN_REFUSED for a reused or retired id (R8-10).
func Redraw(d DataDir) (coreconsensus.NodeID, error) {
	if err := d.offline(); err != nil {
		return 0, err
	}
	return RedrawLog(d.Log())
}

// Report is what Inspect reads, read-only.
type Report struct {
	Stamp      coreconsensus.Stamp
	HardState  coreconsensus.HardState
	First      uint64
	Last       uint64
	Snapshots  []coreconsensus.SnapshotMeta
	Quarantine string
	Markers    []string
}

// Inspect reads a data directory without changing it.
func Inspect(d DataDir) (Report, error) {
	if err := d.offline(); err != nil {
		return Report{}, err
	}
	l := d.d.log
	r := Report{Stamp: l.stamp}
	if q, err := os.ReadFile(filepath.Join(d.d.dir, markerQuarantine)); err == nil {
		r.Quarantine = strings.TrimSpace(string(q))
	}
	if l.refuse == nil {
		r.HardState, _ = l.HardState()
		r.First, r.Last = l.Bounds()
	}
	r.Snapshots, _ = d.d.snaps.List()
	ents, _ := os.ReadDir(d.d.dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), markerStopPrefix) && !strings.HasSuffix(e.Name(), ".tmp") {
			r.Markers = append(r.Markers, e.Name())
		}
	}
	return r, nil
}

// QuarantineExit is one of the two exits ClearQuarantine allows (R3-09).
type QuarantineExit uint8

const (
	// WipeAndRejoin wipes the directory; the operator then Removes the old
	// NodeID and Inits under a NEW one (R4-03). A voter's incarnation is
	// never swapped in place.
	WipeAndRejoin QuarantineExit = iota + 1
	// TruncateAndStaySilent truncates at the damage and persists a silence
	// condition (R4-03).
	TruncateAndStaySilent
)

// ClearQuarantine clears a quarantined, or stopped, directory through one of
// the two exits. There is no third exit that keeps the damaged node voting.
func ClearQuarantine(d DataDir, exit QuarantineExit, op coreconsensus.Operator) error {
	if err := d.offline(); err != nil {
		return err
	}
	if !op.Valid() {
		return misconfigured("Operator", "invalid")
	}
	switch exit {
	case WipeAndRejoin:
	case TruncateAndStaySilent:
		// Not shipped yet: the persisted silence condition and its gate in
		// the step function are not written. Refused, never approximated.
		return origin(coreconsensus.RecoverRefused, errs.String("why", "TruncateAndStaySilent is not implemented"))
	default:
		return misconfigured("exit", "unknown")
	}
	if err := d.audit(op, "clear-quarantine wipe-and-rejoin"); err != nil {
		return err
	}
	_ = d.d.log.Close()
	for _, name := range []string{"log", "snap"} {
		if err := os.RemoveAll(filepath.Join(d.d.dir, name)); err != nil {
			return storageFailed("wipe", err)
		}
	}
	ents, _ := os.ReadDir(d.d.dir)
	for _, e := range ents {
		n := e.Name()
		if n == fileStamp || n == markerQuarantine || strings.HasPrefix(n, markerStopPrefix) {
			if err := os.Remove(filepath.Join(d.d.dir, n)); err != nil {
				return storageFailed("wipe", err)
			}
		}
	}
	_ = os.Remove(d.d.log.runtimeMarker("STORAGE_FAILED"))
	if err := syncDir(d.d.dir); err != nil {
		return storageFailed("wipe", err)
	}
	return d.d.open()
}

// RecoverOptions are Recover's preconditions (R2-25, R3-02, R4-04, R9-08).
type RecoverOptions struct {
	// Confirm must equal the new cluster id in hex: a typed confirmation.
	Confirm string
	// Operator is recorded in the audit file.
	Operator coreconsensus.Operator
	// Fenced attests every old voter left out of the new members.
	Fenced []coreconsensus.FencedAttestation
	// WitnessKeys verify the receipts: one per witness, each must sign.
	WitnessKeys []ed25519.PublicKey
}

// receiptMessage is what a witness signs: the old cluster, the new cluster
// and the fenced voter.
func receiptMessage(old, next coreconsensus.ClusterID, voter coreconsensus.NodeID) []byte {
	b := append(append([]byte("kitsunium-consensus/fenced/v1"), old[:]...), next[:]...)
	return binary.LittleEndian.AppendUint64(b, uint64(voter))
}

// ReceiptMessage returns the bytes a witness signs to attest a fenced voter.
func ReceiptMessage(old, next coreconsensus.ClusterID, voter coreconsensus.NodeID) []byte {
	return receiptMessage(old, next, voter)
}

// Recover starts a NEW history from this node's committed log: a new
// cluster id that records its predecessor and the last index it kept, and a
// forced configuration. Every old voter left out must be attested fenced by
// every witness; without witness keys it fails closed.
func Recover(d DataDir, newCluster coreconsensus.ClusterID, members []coreconsensus.Member, opt RecoverOptions) error {
	if err := d.offline(); err != nil {
		return err
	}
	l := d.d.log
	refused := func(why string) error { return origin(coreconsensus.RecoverRefused, errs.String("why", why)) }
	if l.refuse != nil {
		return refused("quarantined log")
	}
	st := l.stamp
	switch {
	case !opt.Operator.Valid():
		return refused("operator")
	case opt.Confirm != newCluster.String():
		return refused("confirmation does not name the new cluster")
	case newCluster.IsZero() || newCluster == st.Log.Cluster:
		return refused("the new cluster must be new")
	case st.State != coreconsensus.StampMember:
		return refused("not a member directory")
	case len(opt.WitnessKeys) == 0:
		return refused("no witness key")
	}
	self := false
	for _, m := range members {
		if m.SPKI == ([32]byte{}) || m.ID == 0 {
			return refused("a member without an SPKI pin")
		}
		if m.ID == st.ID && m.Voter {
			self = m.Incarnation == st.Incarnation
		}
	}
	if !self {
		return refused("this node is not a voter of the new members")
	}
	hs, _ := l.HardState()
	old := lastMembership(l, d.d.snaps, hs.Commit)
	for _, v := range old.Voters() {
		if slices.ContainsFunc(members, func(m coreconsensus.Member) bool { return m.ID == v }) {
			continue
		}
		i := slices.IndexFunc(opt.Fenced, func(a coreconsensus.FencedAttestation) bool { return a.Voter == v })
		if i < 0 {
			return refused("an old voter is not attested fenced")
		}
		a := opt.Fenced[i]
		msg := receiptMessage(st.Log.Cluster, newCluster, v)
		for _, k := range opt.WitnessKeys {
			if !slices.ContainsFunc(a.Receipts, func(sig []byte) bool { return ed25519.Verify(k, msg, sig) }) {
				return refused("a witness did not sign the fence of an old voter")
			}
		}
	}
	if err := d.audit(opt.Operator, "recover "+newCluster.String()); err != nil {
		return err
	}
	commit := hs.Commit
	first, last := l.Bounds()
	if last > commit {
		if err := l.TruncateSuffix(commit + 1); err != nil {
			return err
		}
	}
	var chain uint32
	var termAt uint64
	if commit >= first {
		es, err := l.Entries(commit, commit+1, 1, nil)
		if err != nil {
			return err
		}
		chain, termAt = es[0].ChainSum, es[0].Position.Term
	} else if metas, _ := d.d.snaps.List(); len(metas) > 0 && metas[0].Position.Index == commit {
		chain, termAt = metas[0].ChainTip, metas[0].Position.Term
	}
	term := max(hs.Term, termAt) + 1
	conf := coreconsensus.Membership{Members: slices.Clone(members), Retired: old.Retired}
	data := wire.AppendAdmin(nil, &wire.Admin{Operator: string(opt.Operator), Change: wire.ChangeForced, Membership: conf})
	e := coreconsensus.Entry{Position: coreconsensus.Position{Term: term, Index: commit + 1}, Kind: coreconsensus.EntryMembership,
		BodySum: wire.BodySum(data), Data: data}
	e.ChainSum = wire.ChainSum(chain, &e)
	nhs := coreconsensus.HardState{Term: term, Commit: commit + 1}
	if err := l.Append([]coreconsensus.Entry{e}, &nhs); err != nil {
		return err
	}
	if err := l.Sync(); err != nil {
		return err
	}
	st.Log = coreconsensus.LogIdentity{Cluster: newCluster, Epoch: 1, Predecessor: st.Log.Cluster,
		PredecessorEpoch: st.Log.Epoch, KeptIndex: commit}
	return l.WriteStamp(st)
}

// lastMembership is the configuration committed at index, from the log or
// the latest snapshot.
func lastMembership(l *FileLog, snaps *FileSnapshots, index uint64) coreconsensus.Membership {
	var m coreconsensus.Membership
	if metas, _ := snaps.List(); len(metas) > 0 {
		m = metas[0].Membership
	}
	first, last := l.Bounds()
	if last >= first {
		es, _ := l.Entries(first, min(last, index)+1, int(^uint(0)>>1), nil)
		for _, e := range es {
			if e.Kind == coreconsensus.EntryMembership {
				if a, err := wire.ParseAdmin(e.Data); err == nil {
					m = a.Membership
				}
			}
		}
	}
	return m
}

// RestoreOptions are RestoreSnapshot's preconditions (R3-15).
type RestoreOptions struct {
	// Expected is the snapshot's metadata, from SaveSnapshot: non-zero.
	Expected coreconsensus.SnapshotMeta
	// Confirm must equal the new cluster id in hex.
	Confirm string
	// Operator is recorded in the audit file.
	Operator coreconsensus.Operator
	// MaxEncoded and MaxDecoded bound what is read.
	MaxEncoded, MaxDecoded int64
	// Codec decodes the chunks; nil is flate.
	Codec coretransform.BoundedDecompressor
}

// RestoreSnapshot starts a new history from a saved snapshot on an empty,
// joining directory, without a Node (R3-15).
func RestoreSnapshot(d DataDir, r io.Reader, newCluster coreconsensus.ClusterID, members []coreconsensus.Member, opt RestoreOptions) (coreconsensus.SnapshotMeta, error) {
	if err := d.offline(); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	refused := func(why string) (coreconsensus.SnapshotMeta, error) {
		return coreconsensus.SnapshotMeta{}, origin(coreconsensus.RestoreRefused, errs.String("why", why))
	}
	l := d.d.log
	st := l.stamp
	exp := opt.Expected
	first, last := l.Bounds()
	switch {
	case l.refuse != nil:
		return refused("quarantined log")
	case !opt.Operator.Valid():
		return refused("operator")
	case opt.Confirm != newCluster.String() || newCluster.IsZero():
		return refused("confirmation does not name the new cluster")
	case exp.ID == "" || exp.Position.Index == 0 || exp.SHA256 == ([32]byte{}):
		return refused("expected metadata is zero")
	case st.State != coreconsensus.StampJoining || last >= first:
		return refused("not an empty joining directory")
	case opt.MaxEncoded <= 0 || opt.MaxDecoded <= 0 || exp.EncodedSize > opt.MaxEncoded || exp.DecodedSize > opt.MaxDecoded:
		return refused("size bounds")
	}
	if !slices.ContainsFunc(members, func(m coreconsensus.Member) bool {
		return m.ID == st.ID && m.Voter && m.Incarnation == st.Incarnation
	}) {
		return refused("this node is not a voter of the new members")
	}
	for _, m := range members {
		if m.SPKI == ([32]byte{}) {
			return refused("a member without an SPKI pin")
		}
	}
	codec := opt.Codec
	if codec == nil {
		codec = defaultCodec()
	}
	if err := d.audit(opt.Operator, "restore-snapshot "+newCluster.String()); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	nl := coreconsensus.LogIdentity{Cluster: newCluster, Epoch: 1, Predecessor: exp.Log.Cluster,
		PredecessorEpoch: exp.Log.Epoch, KeptIndex: exp.Position.Index}
	meta := exp
	meta.Log = nl
	meta.Membership = coreconsensus.Membership{Index: exp.Position.Index, Members: slices.Clone(members), Retired: exp.Membership.Retired}
	sink, off, err := d.d.snaps.Create(meta.ID, meta)
	if err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	if off != 0 {
		_ = sink.Abort()
		if sink, _, err = d.d.snaps.Create(meta.ID, meta); err != nil {
			return coreconsensus.SnapshotMeta{}, err
		}
	}
	n, err := io.Copy(sink, io.LimitReader(r, exp.EncodedSize+1))
	if err != nil || n != exp.EncodedSize {
		_ = sink.Abort()
		return refused("stream size")
	}
	if err := sink.Commit(meta); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	h, err := d.d.snaps.Open(meta.ID)
	if err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	verr := verifySnapshot(io.NewSectionReader(h, 0, meta.EncodedSize), meta, codec, opt.MaxDecoded)
	_ = h.Close()
	if verr != nil {
		_ = d.d.snaps.Remove(meta.ID)
		return coreconsensus.SnapshotMeta{}, verr
	}
	if err := l.resetTo(meta.Position.Index, meta.Position.Term); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	hs := coreconsensus.HardState{Term: meta.Position.Term + 1, Commit: meta.Position.Index}
	if err := l.Append(nil, &hs); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	if err := l.Sync(); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	st.Log, st.State = nl, coreconsensus.StampMember
	if err := l.WriteStamp(st); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	return meta, nil
}

// resetTo makes an empty log start after (index, term).
func (l *FileLog) resetTo(index, term uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.add(&record{kind: recCompact, batch: l.batch, index: index, term: term})
	l.slots = nil
	l.first, l.prevTerm = index+1, term
	l.compact, l.compactTerm = index, term
	return nil
}
