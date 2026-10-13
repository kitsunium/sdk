package consensus

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
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
	q, err := os.ReadFile(filepath.Join(d.d.dir, markerQuarantine))
	switch {
	case err == nil:
		r.Quarantine = strings.TrimSpace(string(q))
	case !errors.Is(err, os.ErrNotExist):
		// A report that could not read the marker must not read as a
		// directory with no quarantine.
		return Report{}, storageFailed("inspect quarantine", err)
	}
	if l.refuse == nil {
		// The hard state as the log last held it, read even after a failed
		// write: Inspect reports what is there, and the directory a failed
		// node left behind is the one an operator inspects.
		l.mu.RLock()
		r.HardState = l.hs
		l.mu.RUnlock()
		r.First, r.Last = l.Bounds()
	}
	// An operator decides from this report: a list that could not be read
	// fails it rather than reading as "no snapshot" or "no stop marker".
	if r.Snapshots, err = d.d.snaps.List(); err != nil {
		return Report{}, err
	}
	ents, err := os.ReadDir(d.d.dir)
	if err != nil {
		return Report{}, storageFailed("inspect markers", err)
	}
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
		if err := d.audit(op, "clear-quarantine truncate-and-stay-silent"); err != nil {
			return err
		}
		return d.truncateAndStaySilent()
	default:
		return misconfigured("exit", "unknown")
	}
	if err := d.audit(op, "clear-quarantine wipe-and-rejoin"); err != nil {
		return err
	}
	if err := d.d.log.Close(); err != nil {
		return err
	}
	for _, name := range []string{"log", "snap"} {
		if err := os.RemoveAll(filepath.Join(d.d.dir, name)); err != nil {
			return storageFailed("wipe", err)
		}
	}
	// A directory that could not be listed keeps its stamp and its markers:
	// the wipe fails here rather than reopen a directory it did not clear.
	ents, err := os.ReadDir(d.d.dir)
	if err != nil {
		return storageFailed("wipe", err)
	}
	for _, e := range ents {
		n := e.Name()
		if n == fileStamp || n == markerQuarantine || strings.HasPrefix(n, markerStopPrefix) {
			if err := os.Remove(filepath.Join(d.d.dir, n)); err != nil {
				return storageFailed("wipe", err)
			}
		}
	}
	// The boot-scoped marker exists only after a storage failure of this
	// boot; one that cannot be removed would refuse the next start anyway,
	// so the wipe says so now.
	if err := os.Remove(d.d.log.runtimeMarker("STORAGE_FAILED")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storageFailed("wipe", err)
	}
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
	if sharedKey(members) {
		return refused("two members under one key")
	}
	if !self {
		return refused("this node is not a voter of the new members")
	}
	hs, err := l.HardState()
	if err != nil {
		return err
	}
	// The old configuration decides which voters must be attested fenced:
	// one that could not be read fails Recover closed, never open.
	old, err := lastMembership(l, d.d.snaps, hs.Commit)
	if err != nil {
		return err
	}
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
	} else {
		metas, err := d.d.snaps.List()
		if err != nil {
			return err
		}
		if len(metas) > 0 && metas[0].Position.Index == commit {
			chain, termAt = metas[0].ChainTip, metas[0].Position.Term
		}
	}
	term := max(hs.Term, termAt) + 1
	conf := coreconsensus.Membership{Members: byID(members), Retired: old.Retired}
	data := wire.AppendAdmin(nil, &wire.Admin{Operator: string(opt.Operator), Change: wire.ChangeForced, Membership: conf})
	e := coreconsensus.Entry{
		Position: coreconsensus.Position{Term: term, Index: commit + 1}, Kind: coreconsensus.EntryMembership,
		BodySum: wire.BodySum(data), Data: data,
	}
	e.ChainSum = wire.ChainSum(chain, &e)
	nhs := coreconsensus.HardState{Term: term, Commit: commit + 1}
	if err := l.Append([]coreconsensus.Entry{e}, &nhs); err != nil {
		return err
	}
	if err := l.Sync(); err != nil {
		return err
	}
	st.Log = coreconsensus.LogIdentity{
		Cluster: newCluster, Epoch: 1, Predecessor: st.Log.Cluster,
		PredecessorEpoch: st.Log.Epoch, KeptIndex: commit,
	}
	return l.WriteStamp(st)
}

// lastMembership is the configuration committed at index, from the log or
// the latest snapshot. A store that cannot answer fails it: a configuration
// read from half the sources is an older one, and Recover would ask for the
// fence of the wrong voters.
func lastMembership(l *FileLog, snaps *FileSnapshots, index uint64) (coreconsensus.Membership, error) {
	var m coreconsensus.Membership
	metas, err := snaps.List()
	if err != nil {
		return m, err
	}
	if len(metas) > 0 {
		m = metas[0].Membership
	}
	first, last := l.Bounds()
	if last >= first {
		es, err := l.Entries(first, min(last, index)+1, int(^uint(0)>>1), nil)
		if err != nil {
			return coreconsensus.Membership{}, err
		}
		for _, e := range es {
			if e.Kind == coreconsensus.EntryMembership {
				if a, err := wire.ParseAdmin(e.Data); err == nil {
					m = a.Membership
				}
			}
		}
	}
	return m, nil
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
	if sharedKey(members) {
		return refused("two members under one key")
	}
	codec := opt.Codec
	if codec == nil {
		codec = defaultCodec()
	}
	if err := d.audit(opt.Operator, "restore-snapshot "+newCluster.String()); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	nl := coreconsensus.LogIdentity{
		Cluster: newCluster, Epoch: 1, Predecessor: exp.Log.Cluster,
		PredecessorEpoch: exp.Log.Epoch, KeptIndex: exp.Position.Index,
	}
	meta := exp
	meta.Log = nl
	meta.Membership = coreconsensus.Membership{Index: exp.Position.Index, Members: slices.Clone(members), Retired: exp.Membership.Retired}
	sink, off, err := d.d.snaps.Create(meta.ID, meta)
	if err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	if off != 0 {
		// A stale spool of that id: discarded, or the fresh one would
		// resume from it.
		if err := sink.Abort(); err != nil {
			return coreconsensus.SnapshotMeta{}, err
		}
		if sink, _, err = d.d.snaps.Create(meta.ID, meta); err != nil {
			return coreconsensus.SnapshotMeta{}, err
		}
	}
	n, err := io.Copy(sink, io.LimitReader(r, exp.EncodedSize+1))
	if err != nil || n != exp.EncodedSize {
		m, rerr := refused("stream size")
		return m, alsoFailed(rerr, "abort", sink.Abort())
	}
	if err := sink.Commit(meta); err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	h, err := d.d.snaps.Open(meta.ID)
	if err != nil {
		return coreconsensus.SnapshotMeta{}, err
	}
	verr := verifySnapshot(io.NewSectionReader(h, 0, meta.EncodedSize), meta, codec, opt.MaxDecoded)
	cerr := h.Close()
	if verr != nil {
		// The copy failed its checks: unpublished, or the failure to do
		// so is reported with it.
		return coreconsensus.SnapshotMeta{}, alsoFailed(verr, "remove", d.d.snaps.Remove(meta.ID))
	}
	if cerr != nil {
		return coreconsensus.SnapshotMeta{}, cerr
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
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.add(&record{kind: recCompact, batch: l.batch, index: index, term: term})
	l.slots = nil
	l.first, l.prevTerm = index+1, term
	l.compact, l.compactTerm = index, term
	return nil
}

const fileSilent = "SILENT"

// truncateAndStaySilent truncates the log at the quarantined damage and
// PERSISTS, before it returns, the silence condition: the highest term any
// readable record of the log names, and the truncation point (R3-09,
// R4-03). Its cost is stated in the ADR: with one of the two other nodes
// down, no leader can exist and the node stays silent until the third
// returns.
func (d DataDir) truncateAndStaySilent() error {
	q, err := os.ReadFile(filepath.Join(d.d.dir, markerQuarantine))
	if err != nil {
		return origin(coreconsensus.RecoverRefused, errs.String("why", "no quarantine marker"))
	}
	var segN uint64
	var off int64
	if _, err := fmt.Sscanf(string(q), "seg-%016x %d", &segN, &off); err != nil {
		return origin(coreconsensus.RecoverRefused, errs.String("why", "unreadable quarantine marker"))
	}
	logDir := filepath.Join(d.d.dir, "log")
	ents, err := os.ReadDir(logDir)
	if err != nil {
		return storageFailed("readdir", err)
	}
	var maxTerm uint64
	for _, e := range ents {
		var n uint64
		if _, err := fmt.Sscanf(e.Name(), "seg-%016x", &n); err != nil || e.Name() != segName(n) {
			continue
		}
		path := filepath.Join(logDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return storageFailed("read segment", err)
		}
		// The highest term of every readable record, before and after the
		// damage: a vote the truncation drops is still never repeated.
		for o := 0; o+recHeader+recFixed <= len(b); o += 8 {
			if r, ok := parseRecord(b[o:]); ok {
				maxTerm = max(maxTerm, r.term)
				if r.kind == recHardState && len(r.data) == 24 {
					maxTerm = max(maxTerm, binary.LittleEndian.Uint64(r.data))
				}
				if r.kind == recCheckpt {
					if cp, ok := decodeCheckpoint(r.data); ok {
						maxTerm = max(maxTerm, cp.hs.Term)
					}
				}
				o += r.length - 8
			}
		}
		switch {
		case n > segN || (n == segN && off == 0):
			if err := os.Remove(path); err != nil {
				return storageFailed("truncate", err)
			}
		case n == segN:
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return storageFailed("truncate", err)
			}
			err = zeroFrom(f, off, int64(len(b)))
			if cerr := f.Close(); cerr != nil && err == nil {
				err = cerr
			}
			if err != nil {
				return storageFailed("truncate", err)
			}
		}
	}
	if err := syncDir(logDir); err != nil {
		return storageFailed("truncate", err)
	}
	// The condition is durable BEFORE the marker goes: a crash in between
	// leaves a silent node, never a voting one. Its index is filled in
	// once the truncated log is read (0 means "the end of the log").
	if err := writeFileSync(filepath.Join(d.d.dir, fileSilent), binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, maxTerm), 0)); err != nil {
		return storageFailed("silence condition", err)
	}
	if err := os.Remove(filepath.Join(d.d.dir, markerQuarantine)); err != nil {
		return storageFailed("clear marker", err)
	}
	if err := d.d.log.Close(); err != nil {
		return err
	}
	if err := d.d.open(); err != nil {
		return err
	}
	_, last := d.d.log.Bounds()
	b := binary.LittleEndian.AppendUint64(nil, maxTerm)
	b = binary.LittleEndian.AppendUint64(b, last+1)
	if err := writeFileSync(filepath.Join(d.d.dir, fileSilent), b); err != nil {
		return storageFailed("silence condition", err)
	}
	return syncDir(d.d.dir)
}

// silence reads the persisted silence condition, if any. An index of 0, a
// crash before it was filled in, is the end of the log, written back now.
// An unreadable condition is the safe side: silent past the end.
//
// The write-back is durable before the node starts, or the node does not
// start (D4: a failed write stops it): a condition left at 0 would be filled
// in again at the next start, from a log that may have grown since, and the
// silence would end at another index than the one this start obeyed.
func (l *FileLog) silence() (term, index uint64, err error) {
	b, err := os.ReadFile(filepath.Join(l.dir, fileSilent))
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	_, last := l.Bounds()
	if err != nil || len(b) != 16 {
		return ^uint64(0) >> 1, last + 1, nil
	}
	term, index = binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:])
	if index == 0 {
		index = last + 1
		nb := binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, term), index)
		if err := writeFileSync(filepath.Join(l.dir, fileSilent), nb); err != nil {
			return 0, 0, err
		}
	}
	return term, index, nil
}

// endSilence removes the silence condition durably.
func (l *FileLog) endSilence() error {
	if err := os.Remove(filepath.Join(l.dir, fileSilent)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(l.dir)
}
