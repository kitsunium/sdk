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

const (
	// WipeAndRejoin wipes the directory; the operator then Removes the old
	// NodeID and Inits under a NEW one (R4-03). A voter's incarnation is
	// never swapped in place.
	WipeAndRejoin QuarantineExit = iota + 1
	// TruncateAndStaySilent truncates at the damage and persists a silence
	// condition (R4-03).
	TruncateAndStaySilent
)

const (
	// fileSilent holds the persisted silence condition: the highest term the
	// truncated log ever named and the index the silence lasts until (R4-03).
	fileSilent = "SILENT"
	// fileAudit is the append-only record of every offline operation.
	fileAudit = "AUDIT"
)

// The offline functions work on the shipped layout, and only on it (D17,
// R2-33), on a stopped node: each refuses a DataDir a live Node holds.

// offline guards an offline operation: refused while a live Node holds the
// directory (D11).
func (d DataDir) offline() error {
	if d.d == nil {
		return misconfigured("DataDir", "zero")
	}
	d.d.mu.Lock()
	defer d.d.mu.Unlock()
	switch {
	case d.d.closed:
		return origin(coreconsensus.NodeStopped, errs.String("why", "data directory closed"))
	case d.d.inUse:
		return origin(coreconsensus.DataDirInUse, errs.String("dir", d.d.dir))
	}
	return nil
}

// audit appends one line to the operator audit file and syncs it before the
// operation it records runs. Its Close is part of the write: a failure there
// is a storage failure like the sync's, never a detail dropped at return.
func (d DataDir) audit(op coreconsensus.Operator, what string) (err error) {
	f, err := os.OpenFile(filepath.Join(d.d.dir, fileAudit), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return storageFailed("audit", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = storageFailed("audit", cerr)
		}
	}()
	if _, err := fmt.Fprintf(f, "%s %s\n", op, what); err != nil {
		return storageFailed("audit", err)
	}
	if err := f.Sync(); err != nil {
		return storageFailed("audit", err)
	}
	return nil
}

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

// ReportValue is what Inspect reads, read-only: the stamp, the hard state
// and bounds of the log, the complete snapshots, and the markers that would
// refuse a start.
type ReportValue struct {
	Stamp      coreconsensus.StampValue
	HardState  coreconsensus.HardState
	First      uint64
	Last       uint64
	Snapshots  []coreconsensus.SnapshotMetaValue
	Quarantine string
	Markers    []string
}

// Inspect reads a data directory without changing it.
func Inspect(d DataDir) (ReportValue, error) {
	if err := d.offline(); err != nil {
		return ReportValue{}, err
	}
	l := d.d.log
	r := ReportValue{Stamp: l.stamp}
	q, err := readQuarantineMarker(d.d.dir)
	if err != nil {
		return ReportValue{}, err
	}
	r.Quarantine = q
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
		return ReportValue{}, err
	}
	if r.Markers, err = stopMarkers(d.d.dir); err != nil {
		return ReportValue{}, err
	}
	return r, nil
}

// readQuarantineMarker is the quarantine marker of dir, trimmed, or "" when
// there is none. A marker that could not be read is a STORAGE_FAILED: a
// report must not read as a directory with no quarantine.
func readQuarantineMarker(dir string) (string, error) {
	q, err := os.ReadFile(filepath.Join(dir, markerQuarantine))
	switch {
	case err == nil:
		return strings.TrimSpace(string(q)), nil
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	default:
		return "", storageFailed("inspect quarantine", err)
	}
}

// stopMarkers names the stop markers of dir, nil when there is none; a .tmp
// is a marker whose write never completed, and is not one.
func stopMarkers(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, storageFailed("inspect markers", err)
	}
	var markers []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), markerStopPrefix) && !strings.HasSuffix(e.Name(), ".tmp") {
			markers = append(markers, e.Name())
		}
	}
	return markers, nil
}

// QuarantineExit is one of the two exits ClearQuarantine allows (R3-09).
type QuarantineExit uint8

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
		return d.wipeAndRejoin(op)
	case TruncateAndStaySilent:
		if err := d.audit(op, "clear-quarantine truncate-and-stay-silent"); err != nil {
			return err
		}
		return d.truncateAndStaySilent()
	default:
		return misconfigured("exit", "unknown")
	}
}

// wipeAndRejoin is the WipeAndRejoin exit: audited first, then the log is
// closed, its stores, stamp and markers are deleted, the directory synced,
// and the directory reopened empty.
func (d DataDir) wipeAndRejoin(op coreconsensus.Operator) error {
	if err := d.audit(op, "clear-quarantine wipe-and-rejoin"); err != nil {
		return err
	}
	if err := d.d.log.Close(); err != nil {
		return err
	}
	if err := d.removeStores(); err != nil {
		return err
	}
	if err := d.removeStampAndMarkers(); err != nil {
		return err
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

// removeStores deletes the log and the snapshots, whole.
func (d DataDir) removeStores() error {
	for _, name := range []string{"log", "snap"} {
		if err := os.RemoveAll(filepath.Join(d.d.dir, name)); err != nil {
			return storageFailed("wipe", err)
		}
	}
	return nil
}

// removeStampAndMarkers deletes the stamp, the quarantine marker and every
// stop marker. A directory that could not be listed keeps its stamp and its
// markers: the wipe fails here rather than reopen a directory it did not
// clear.
func (d DataDir) removeStampAndMarkers() error {
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
	return nil
}

// RecoverOptions are Recover's preconditions (R2-25, R3-02, R4-04, R9-08).
// Recover refuses, before it writes anything, an invalid operator, a
// confirmation that does not name the new cluster, no witness key, and an
// old voter left out without a receipt from every witness.
type RecoverOptions struct {
	// Confirm must equal the new cluster id in hex: a typed confirmation.
	Confirm string
	// Operator is recorded in the audit file.
	Operator coreconsensus.Operator
	// Fenced attests every old voter left out of the new members.
	Fenced []coreconsensus.FencedAttestationValue
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

// Recover starts a NEW history from this node's log: every entry up to its
// persisted commit, then every entry past it whose body and chain verify, cut
// at the first broken link, so no write the old cluster acknowledged is lost;
// a new cluster id that records its predecessor and the last index it kept;
// and a forced configuration. The audit file records how many entries past
// the persisted commit were adopted. Every old voter left out must be
// attested fenced by every witness; without witness keys it fails closed.
func Recover(d DataDir, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RecoverOptions) error {
	if err := d.offline(); err != nil {
		return err
	}
	flog := d.d.log
	if flog.refuse != nil {
		return recoverRefused("quarantined log")
	}
	stamp := flog.stamp
	if err := checkRecover(&stamp, newCluster, opt); err != nil {
		return err
	}
	if err := checkRecoverMembers(&stamp, members); err != nil {
		return err
	}
	hard, err := flog.HardState()
	if err != nil {
		return err
	}
	// The new history keeps every entry this survivor holds whose chain
	// verifies, not only those up to its persisted commit (issue #399).
	kept, err := d.adoptedEnd(hard.Commit)
	if err != nil {
		return err
	}
	// The old configuration decides which voters must be attested fenced:
	// one that could not be read fails Recover closed, never open. It is
	// the configuration of the last entry kept: an adopted change counts.
	old, err := lastMembership(flog, d.d.snaps, kept)
	if err != nil {
		return err
	}
	if err := checkFenced(old.Voters(), members, opt, stamp.Log.Cluster, newCluster); err != nil {
		return err
	}
	// The audit line is Recover's report: the index kept, the persisted
	// commit, and how many entries past that commit the new history adopts.
	what := fmt.Sprintf("recover %s kept %d commit %d adopted %d", newCluster, kept, hard.Commit, kept-hard.Commit)
	if err := d.audit(opt.Operator, what); err != nil {
		return err
	}
	if err := d.appendForced(new(coreconsensus.HardState{Term: hard.Term, Commit: kept}), members, &old, opt.Operator); err != nil {
		return err
	}
	stamp.Log = coreconsensus.LogIdentityValue{
		Cluster: newCluster, Epoch: 1, Predecessor: stamp.Log.Cluster,
		PredecessorEpoch: stamp.Log.Epoch, KeptIndex: kept,
	}
	return flog.WriteStamp(stamp)
}

// recoverRefused is Recover's refusal, its reason in the why field.
func recoverRefused(why string) error {
	return origin(coreconsensus.RecoverRefused, errs.String("why", why))
}

// isZeroDigest reports whether d is the all-zero digest, which no real
// SPKI pin or snapshot hash is.
func isZeroDigest(d [32]byte) bool {
	var zero [32]byte
	return d == zero
}

// checkRecover refuses a Recover whose preconditions fail, in a fixed order
// so the reason given is always the first that applies.
func checkRecover(stamp *coreconsensus.StampValue, newCluster coreconsensus.ClusterID, opt *RecoverOptions) error {
	switch {
	case !opt.Operator.Valid():
		return recoverRefused("operator")
	case opt.Confirm != newCluster.String():
		return recoverRefused("confirmation does not name the new cluster")
	case newCluster.IsZero() || newCluster == stamp.Log.Cluster:
		return recoverRefused("the new cluster must be new")
	case stamp.State != coreconsensus.StampMember:
		return recoverRefused("not a member directory")
	case len(opt.WitnessKeys) == 0:
		return recoverRefused("no witness key")
	}
	return nil
}

// checkRecoverMembers refuses new members that are not all pinned, or
// among which this node, at its incarnation, is not a voter.
func checkRecoverMembers(stamp *coreconsensus.StampValue, members []coreconsensus.MemberValue) error {
	self := false
	for _, m := range members {
		if isZeroDigest(m.SPKI) || m.ID == 0 {
			return recoverRefused("a member without an SPKI pin")
		}
		if m.ID == stamp.ID && m.Voter {
			self = m.Incarnation == stamp.Incarnation
		}
	}
	if sharedKey(members) {
		return recoverRefused("two members under one key")
	}
	if !self {
		return recoverRefused("this node is not a voter of the new members")
	}
	return nil
}

// checkFenced refuses a Recover that leaves out an old voter without a
// fence receipt from EVERY witness for it.
func checkFenced(voters []coreconsensus.NodeID, members []coreconsensus.MemberValue, opt *RecoverOptions, oldCluster, newCluster coreconsensus.ClusterID) error {
	for _, v := range voters {
		if slices.ContainsFunc(members, func(m coreconsensus.MemberValue) bool { return m.ID == v }) {
			continue
		}
		i := slices.IndexFunc(opt.Fenced, func(a coreconsensus.FencedAttestationValue) bool { return a.Voter == v })
		if i < 0 {
			return recoverRefused("an old voter is not attested fenced")
		}
		msg := receiptMessage(oldCluster, newCluster, v)
		if !signedByEvery(opt.WitnessKeys, opt.Fenced[i].Receipts, msg) {
			return recoverRefused("a witness did not sign the fence of an old voter")
		}
	}
	return nil
}

// signedByEvery reports whether every key signed msg in one of receipts.
func signedByEvery(keys []ed25519.PublicKey, receipts [][]byte, msg []byte) bool {
	for _, k := range keys {
		if !slices.ContainsFunc(receipts, func(sig []byte) bool { return ed25519.Verify(k, msg, sig) }) {
			return false
		}
	}
	return true
}

// adoptedEnd is the last index Recover keeps: the persisted commit, then
// every entry past it whose body and chain verify from the chain at that
// commit, up to the first broken link.
//
// The persisted commit lags the cluster's: a follower records a commit one
// round after its leader acknowledged it, and a batch caps the commit it
// carries. An entry past it may therefore be a write the old cluster
// committed and acknowledged; the old cluster is fenced, so the new history
// may adopt it, and cutting it would lose an acknowledged write. The price
// is that an entry whose client was told ENTRY_SUPERSEDED, or never
// answered, can be applied too: a duplicate an owner refuses by
// precondition, never a loss (ADR 0152 D17).
//
// An entry whose record no longer parses (LOG_CORRUPT) is a broken link and
// ends the suffix; any other read failure fails Recover closed.
func (d DataDir) adoptedEnd(commit uint64) (uint64, error) {
	flog := d.d.log
	first, last := flog.Bounds()
	if last <= commit || commit+1 < first {
		return commit, nil
	}
	chain, term, err := d.chainAt(commit, first)
	if err != nil {
		return 0, err
	}
	var buf []coreconsensus.EntryValue
	end := commit
	for end < last {
		es, err := flog.Entries(end+1, end+2, 1, buf[:0])
		if errors.Is(err, coreconsensus.LogCorrupt) {
			return end, nil
		}
		if err != nil {
			return 0, err
		}
		e := &es[0]
		if !extends(e, end, term, chain) {
			return end, nil
		}
		chain, term, end = e.ChainSum, e.Position.Term, e.Position.Index
		buf = es
	}
	return end, nil
}

// extends reports whether e is the verified link after the entry at end,
// of term term and chain sum chain: the next index, a term that does not go
// back, and a body and chain sum that match what e records.
func extends(e *coreconsensus.EntryValue, end, term uint64, chain uint32) bool {
	return e.Position.Index == end+1 && e.Position.Term >= term &&
		wire.BodySum(e.Data) == e.BodySum && wire.ChainSum(chain, e) == e.ChainSum
}

// appendForced cuts the log back to hard.Commit, the last index Recover
// adopts, and appends, synced, the forced configuration entry of the new
// history, chained to the entry it follows and one term past any the log
// knew.
func (d DataDir) appendForced(hard *coreconsensus.HardState, members []coreconsensus.MemberValue, old *coreconsensus.MembershipValue, operator coreconsensus.Operator) error {
	flog := d.d.log
	commit := hard.Commit
	first, last := flog.Bounds()
	if last > commit {
		if err := flog.TruncateSuffix(commit + 1); err != nil {
			return err
		}
	}
	chain, termAt, err := d.chainAt(commit, first)
	if err != nil {
		return err
	}
	term := max(hard.Term, termAt) + 1
	conf := coreconsensus.MembershipValue{Members: byID(members), Retired: old.Retired}
	data := wire.AppendAdmin(nil, &wire.AdminValue{Operator: string(operator), Change: wire.ChangeForced, Membership: conf})
	e := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: term, Index: commit + 1}, Kind: coreconsensus.EntryMembership,
		BodySum: wire.BodySum(data), Data: data,
	}
	e.ChainSum = wire.ChainSum(chain, &e)
	if err := flog.Append([]coreconsensus.EntryValue{e}, &coreconsensus.HardState{Term: term, Commit: commit + 1}); err != nil {
		return err
	}
	return flog.Sync()
}

// chainAt is the chain sum and term of the committed entry commit: from the
// log when it still holds it, else from the newest snapshot when that one
// ends exactly there, else zero.
func (d DataDir) chainAt(commit, first uint64) (uint32, uint64, error) {
	if commit >= first {
		es, err := d.d.log.Entries(commit, commit+1, 1, nil)
		if err != nil {
			return 0, 0, err
		}
		return es[0].ChainSum, es[0].Position.Term, nil
	}
	metas, err := d.d.snaps.List()
	if err != nil {
		return 0, 0, err
	}
	if len(metas) > 0 && metas[0].Position.Index == commit {
		return metas[0].ChainTip, metas[0].Position.Term, nil
	}
	return 0, 0, nil
}

// lastMembership is the configuration committed at index, from the log or
// the latest snapshot. A store that cannot answer fails it: a configuration
// read from half the sources is an older one, and Recover would ask for the
// fence of the wrong voters.
func lastMembership(l *fileLog, snaps *fileSnapshots, index uint64) (coreconsensus.MembershipValue, error) {
	var m coreconsensus.MembershipValue
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
			return coreconsensus.MembershipValue{}, err
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

// RestoreOptions are RestoreSnapshot's preconditions (R3-15). Expected and
// both bounds are required; the stream is refused when its size or digest
// differs from Expected.
type RestoreOptions struct {
	// Expected is the snapshot's metadata, from SaveSnapshot: non-zero.
	Expected coreconsensus.SnapshotMetaValue
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
func RestoreSnapshot(d DataDir, r io.Reader, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RestoreOptions) (coreconsensus.SnapshotMetaValue, error) {
	if err := d.offline(); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	flog := d.d.log
	stamp := flog.stamp
	if err := checkRestore(flog, &stamp, newCluster, members, opt); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	codec, err := restoreCodec(opt)
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := d.audit(opt.Operator, "restore-snapshot "+newCluster.String()); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	meta := restoredMeta(&opt.Expected, newCluster, members)
	if err := d.spoolRestored(r, &meta); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := d.verifyRestored(&meta, codec, opt.MaxDecoded); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	if err := flog.restartAt(&meta); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	stamp.Log, stamp.State = meta.Log, coreconsensus.StampMember
	if err := flog.WriteStamp(stamp); err != nil {
		return coreconsensus.SnapshotMetaValue{}, err
	}
	return meta, nil
}

// restoreRefused is RestoreSnapshot's refusal, its reason in the why field.
func restoreRefused(why string) error {
	return origin(coreconsensus.RestoreRefused, errs.String("why", why))
}

// checkRestore refuses a RestoreSnapshot whose preconditions fail, in a
// fixed order so the reason given is always the first that applies: the
// target, then the bounds, then the members.
func checkRestore(flog *fileLog, stamp *coreconsensus.StampValue, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RestoreOptions) error {
	if err := checkRestoreTarget(flog, newCluster, opt); err != nil {
		return err
	}
	if err := checkRestoreBounds(flog, stamp, opt); err != nil {
		return err
	}
	return checkRestoreMembers(stamp, members)
}

// restoreCodec is the codec the restored chunks are decoded with: the
// caller's, else the default, flate.
func restoreCodec(opt *RestoreOptions) (coretransform.BoundedDecompressor, error) {
	if opt.Codec != nil {
		return opt.Codec, nil
	}
	return defaultRestoreCodec()
}

// defaultRestoreCodec is the default for a restore, refused when flate no
// longer bounds its decompression rather than decoding without a bound.
func defaultRestoreCodec() (coretransform.BoundedDecompressor, error) {
	if codec, ok := defaultCodec(); ok {
		return codec, nil
	}
	return nil, misconfigured("Codec", "no bounded decompressor")
}

// checkRestoreTarget is the first half of RestoreSnapshot's preconditions,
// checked in a fixed order with checkRestoreBounds after it, so the reason
// given is always the first that applies.
func checkRestoreTarget(flog *fileLog, newCluster coreconsensus.ClusterID, opt *RestoreOptions) error {
	exp := &opt.Expected
	switch {
	case flog.refuse != nil:
		return restoreRefused("quarantined log")
	case !opt.Operator.Valid():
		return restoreRefused("operator")
	case opt.Confirm != newCluster.String() || newCluster.IsZero():
		return restoreRefused("confirmation does not name the new cluster")
	case exp.ID == "" || exp.Position.Index == 0 || isZeroDigest(exp.SHA256):
		return restoreRefused("expected metadata is zero")
	}
	return nil
}

// checkRestoreBounds is the second half of RestoreSnapshot's preconditions:
// an empty joining directory, and a snapshot within the bounds the caller
// set, both of which must be set.
func checkRestoreBounds(flog *fileLog, stamp *coreconsensus.StampValue, opt *RestoreOptions) error {
	exp := &opt.Expected
	first, last := flog.Bounds()
	switch {
	case stamp.State != coreconsensus.StampJoining || last >= first:
		return restoreRefused("not an empty joining directory")
	case opt.MaxEncoded <= 0 || opt.MaxDecoded <= 0 || exp.EncodedSize > opt.MaxEncoded || exp.DecodedSize > opt.MaxDecoded:
		return restoreRefused("size bounds")
	}
	return nil
}

// checkRestoreMembers refuses new members among which this node, at its
// incarnation, is not a voter, or of which one is not pinned.
func checkRestoreMembers(stamp *coreconsensus.StampValue, members []coreconsensus.MemberValue) error {
	if !slices.ContainsFunc(members, func(m coreconsensus.MemberValue) bool {
		return m.ID == stamp.ID && m.Voter && m.Incarnation == stamp.Incarnation
	}) {
		return restoreRefused("this node is not a voter of the new members")
	}
	for _, m := range members {
		if isZeroDigest(m.SPKI) {
			return restoreRefused("a member without an SPKI pin")
		}
	}
	if sharedKey(members) {
		return restoreRefused("two members under one key")
	}
	return nil
}

// restoredMeta is the metadata the restored snapshot is published under:
// the expected one, in a new history that records the old cluster as its
// predecessor and the snapshot's index as the last it kept, with the new
// members.
func restoredMeta(exp *coreconsensus.SnapshotMetaValue, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue) coreconsensus.SnapshotMetaValue {
	meta := *exp
	meta.Log = coreconsensus.LogIdentityValue{
		Cluster: newCluster, Epoch: 1, Predecessor: exp.Log.Cluster,
		PredecessorEpoch: exp.Log.Epoch, KeptIndex: exp.Position.Index,
	}
	meta.Membership = coreconsensus.MembershipValue{Index: exp.Position.Index, Members: slices.Clone(members), Retired: exp.Membership.Retired}
	return meta
}

// spoolRestored copies exactly meta.EncodedSize bytes of r into a fresh
// spool and commits it. A stream of any other length is refused and its
// spool aborted, the abort's failure kept beside the refusal.
func (d DataDir) spoolRestored(r io.Reader, meta *coreconsensus.SnapshotMetaValue) error {
	sink, off, err := d.d.snaps.Create(meta.ID, *meta)
	if err != nil {
		return err
	}
	if off != 0 {
		// A stale spool of that id: discarded, or the fresh one would
		// resume from it.
		if err := sink.Abort(); err != nil {
			return err
		}
		if sink, _, err = d.d.snaps.Create(meta.ID, *meta); err != nil {
			return err
		}
	}
	n, err := io.Copy(sink, io.LimitReader(r, meta.EncodedSize+1))
	if err != nil || n != meta.EncodedSize {
		return alsoFailed(restoreRefused("stream size"), "abort", sink.Abort())
	}
	return sink.Commit(*meta)
}

// verifyRestored decodes the published copy and checks it against meta. A
// copy that fails is unpublished, or the failure to do so is reported with
// it; the handle's Close failure is reported only when the copy passed.
func (d DataDir) verifyRestored(meta *coreconsensus.SnapshotMetaValue, codec coretransform.BoundedDecompressor, maxDecoded int64) error {
	h, err := d.d.snaps.Open(meta.ID)
	if err != nil {
		return err
	}
	verr := verifySnapshot(io.NewSectionReader(h, 0, meta.EncodedSize), *meta, codec, maxDecoded)
	cerr := h.Close()
	if verr != nil {
		return alsoFailed(verr, "remove", d.d.snaps.Remove(meta.ID))
	}
	return cerr
}

// restartAt makes the empty log start after the restored snapshot, with a
// hard state one term past it, durably.
func (l *fileLog) restartAt(meta *coreconsensus.SnapshotMetaValue) error {
	if err := l.resetTo(meta.Position.Index, meta.Position.Term); err != nil {
		return err
	}
	if err := l.Append(nil, &coreconsensus.HardState{Term: meta.Position.Term + 1, Commit: meta.Position.Index}); err != nil {
		return err
	}
	return l.Sync()
}

// resetTo makes an empty log start after (index, term).
func (l *fileLog) resetTo(index, term uint64) error {
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

// truncateAndStaySilent truncates the log at the quarantined damage and
// PERSISTS, before it returns, the silence condition: the highest term any
// readable record of the log names, and the truncation point (R3-09,
// R4-03). Its cost is stated in the ADR: with one of the two other nodes
// down, no leader can exist and the node stays silent until the third
// returns.
func (d DataDir) truncateAndStaySilent() error {
	segN, off, err := d.quarantinePoint()
	if err != nil {
		return err
	}
	logDir := filepath.Join(d.d.dir, "log")
	maxTerm, err := truncateAtDamage(logDir, segN, off)
	if err != nil {
		return err
	}
	if err := syncDir(logDir); err != nil {
		return storageFailed("truncate", err)
	}
	// The condition is durable BEFORE the marker goes: a crash in between
	// leaves a silent node, never a voting one. Its index is filled in
	// once the truncated log is read (0 means "the end of the log").
	if err := writeFileSync(filepath.Join(d.d.dir, fileSilent), silenceRecord(maxTerm, 0)); err != nil {
		return storageFailed("silence condition", err)
	}
	if err := os.Remove(filepath.Join(d.d.dir, markerQuarantine)); err != nil {
		return storageFailed("clear marker", err)
	}
	return d.reopenSilenced(maxTerm)
}

// quarantinePoint reads where the quarantine marker places the damage. A
// marker that is missing or unreadable refuses the exit: nothing else
// records where the damage is, and the truncation must start exactly there.
func (d DataDir) quarantinePoint() (uint64, int64, error) {
	q, err := os.ReadFile(filepath.Join(d.d.dir, markerQuarantine))
	if err != nil {
		return 0, 0, recoverRefused("no quarantine marker")
	}
	var segN uint64
	var off int64
	if _, err := fmt.Sscanf(string(q), "seg-%016x %d", &segN, &off); err != nil {
		return 0, 0, recoverRefused("unreadable quarantine marker")
	}
	return segN, off, nil
}

// truncateAtDamage deletes every segment after segment segN, and segN too
// when the damage is at its start, zeroes segN from off otherwise, and
// returns the highest term of every readable record, read before anything
// is changed: a vote the truncation drops is still never repeated.
func truncateAtDamage(logDir string, segN uint64, off int64) (uint64, error) {
	ents, err := os.ReadDir(logDir)
	if err != nil {
		return 0, storageFailed("readdir", err)
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
			return 0, storageFailed("read segment", err)
		}
		maxTerm = max(maxTerm, maxRecordTerm(b))
		if err := truncateSegment(path, n, segN, off, int64(len(b))); err != nil {
			return 0, err
		}
	}
	return maxTerm, nil
}

// maxRecordTerm is the highest term any readable record of segment b names,
// before and after any damage, scanning every record boundary.
func maxRecordTerm(b []byte) uint64 {
	var maxTerm uint64
	for o := 0; o+recHeader+recFixed <= len(b); o += recAlign {
		r, ok := parseRecord(b[o:])
		if !ok {
			continue
		}
		maxTerm = max(maxTerm, recordTerm(&r))
		o += r.length - recAlign
	}
	return maxTerm
}

// recordTerm is the highest term r names: its own, and the one of the hard
// state or checkpoint it carries.
func recordTerm(r *record) uint64 {
	term := r.term
	if r.kind == recHardState && len(r.data) == hardStateLen {
		term = max(term, binary.LittleEndian.Uint64(r.data))
	}
	if r.kind == recCheckpt {
		if cp, ok := decodeCheckpoint(r.data); ok {
			term = max(term, cp.hs.Term)
		}
	}
	return term
}

// truncateSegment applies the truncation at (segN, off) to segment n of
// size bytes at path: deleted when it lies wholly past the damage, zeroed
// from off when the damage is inside it, untouched before it.
func truncateSegment(path string, n, segN uint64, off, size int64) error {
	switch {
	case n > segN || (n == segN && off == 0):
		if err := os.Remove(path); err != nil {
			return storageFailed("truncate", err)
		}
	case n == segN:
		return zeroSegmentFrom(path, off, size)
	}
	return nil
}

// zeroSegmentFrom zeroes and syncs the segment at path from off to size.
// Its Close is part of the write: a failure there is reported like the
// zeroing's.
func zeroSegmentFrom(path string, off, size int64) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return storageFailed("truncate", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = storageFailed("truncate", cerr)
		}
	}()
	if err := zeroFrom(f, off, size); err != nil {
		return storageFailed("truncate", err)
	}
	return nil
}

// silenceRecord is the silence condition's on-disk form: the term, then
// the index the silence lasts until, 0 for "the end of the log".
func silenceRecord(term, index uint64) []byte {
	return binary.LittleEndian.AppendUint64(binary.LittleEndian.AppendUint64(nil, term), index)
}

// reopenSilenced reopens the truncated log and fills in the silence
// condition's index, its end, durably.
func (d DataDir) reopenSilenced(maxTerm uint64) error {
	if err := d.d.log.Close(); err != nil {
		return err
	}
	if err := d.d.open(); err != nil {
		return err
	}
	_, last := d.d.log.Bounds()
	if err := writeFileSync(filepath.Join(d.d.dir, fileSilent), silenceRecord(maxTerm, last+1)); err != nil {
		return storageFailed("silence condition", err)
	}
	return syncDir(d.d.dir)
}

// silence reads the persisted silence condition, if any. An index of 0, a
// crash before it was filled in, is the end of the log, written back now.
// An unreadable condition is the safe side: silent past the end, with the
// reason it could not be read in unreadable, for the node to report.
//
// The write-back is durable before the node starts, or the node does not
// start (D4: a failed write stops it): a condition left at 0 would be filled
// in again at the next start, from a log that may have grown since, and the
// silence would end at another index than the one this start obeyed.
func (l *fileLog) silence() (term, index uint64, unreadable, err error) {
	b, err := os.ReadFile(filepath.Join(l.dir, fileSilent))
	if os.IsNotExist(err) {
		return 0, 0, nil, nil
	}
	_, last := l.Bounds()
	if err == nil && len(b) != 16 {
		err = errSilenceLength
	}
	if err != nil {
		return ^uint64(0) >> 1, last + 1, err, nil
	}
	term, index = binary.LittleEndian.Uint64(b), binary.LittleEndian.Uint64(b[8:])
	if index == 0 {
		index = last + 1
		if err := writeFileSync(filepath.Join(l.dir, fileSilent), silenceRecord(term, index)); err != nil {
			return 0, 0, nil, err
		}
	}
	return term, index, nil, nil
}

// endSilence removes the silence condition durably.
func (l *fileLog) endSilence() error {
	if err := os.Remove(filepath.Join(l.dir, fileSilent)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(l.dir)
}
