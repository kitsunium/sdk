package consensus

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
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
	// markerRecover is the stop marker of a Recover in progress: written,
	// synced, before the forced entry, and removed only once the new stamp is
	// durable. It names the new cluster and the last index kept, so a start
	// is refused while it is there and a Recover to that cluster resumes from
	// what the first one decided (#405).
	markerRecover = markerStopPrefix + "RECOVER"
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
	// The first line creates the file: its entry is durable only once the
	// directory is synced (#422).
	if err := syncDir(d.d.dir); err != nil {
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
	Stamp     coreconsensus.StampValue
	HardState coreconsensus.HardState
	First     uint64
	// Last is the last index the directory holds: the log's, or the
	// newest snapshot's when an install a crash interrupted published it
	// past the log's end.
	Last uint64
	// LastTerm is the term of the entry at Last, or of the snapshot that
	// holds Last. With Last it is the key a Recover's survivor is chosen
	// by: the highest (LastTerm, Last), compared in that order, never Last
	// alone nor HardState.Term, the term of the last vote.
	LastTerm   uint64
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
	if l.refuse == nil && len(r.Snapshots) > 0 {
		// An install a crash interrupted published its snapshot past the
		// log's end: the directory holds the state through that index, and
		// an operator comparing survivors must see it (#506).
		r.Last = max(r.Last, r.Snapshots[0].Position.Index)
	}
	if l.refuse == nil {
		r.LastTerm = lastTermOf(l, &r)
	}
	if r.Markers, err = stopMarkers(d.d.dir); err != nil {
		return ReportValue{}, err
	}
	return r, nil
}

// lastTermOf is the term of the entry at r.Last: the newest snapshot's when
// it ends at r.Last, else the log's. It is the half of the survivor's key
// Inspect did not report: two survivors may show the same Last and the same
// HardState.Term, the term of a vote, and only the term of their last
// entries tells which one holds the writes a majority committed (D17,
// #534). A snapshot at r.Last wins over the log's entry there: an install a
// crash interrupted published it past the log, or at the log's last index
// under another term, and the start, or Recover, moves the log onto it
// (#506). A log with no entry and no snapshot reports 0.
func lastTermOf(l *fileLog, r *ReportValue) uint64 {
	if len(r.Snapshots) > 0 && r.Snapshots[0].Position.Index == r.Last {
		return r.Snapshots[0].Position.Term
	}
	if t, err := l.Term(r.Last); err == nil {
		return t
	}
	return 0
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
		if err := d.d.log.storageFailedThisBoot(); err != nil {
			// The truncation would be written after pages the disk may
			// not hold: reboot first (#563). WipeAndRejoin keeps nothing
			// and stays open.
			return err
		}
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

// removeStampAndMarkers deletes the stamp, the quarantine marker, the
// silence condition and every stop marker. A directory that could not be listed keeps its stamp and its
// markers: the wipe fails here rather than reopen a directory it did not
// clear.
func (d DataDir) removeStampAndMarkers() error {
	ents, err := os.ReadDir(d.d.dir)
	if err != nil {
		return storageFailed("wipe", err)
	}
	for _, e := range ents {
		n := e.Name()
		// SILENT goes too: the new member must not start silent at the old
		// log's index (#422).
		if n == fileStamp || n == markerQuarantine || n == fileSilent || strings.HasPrefix(n, markerStopPrefix) {
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

// Recover starts a NEW history from this node's log — run it on the
// survivor whose report from Inspect holds the highest (LastTerm, Last):
// every entry up to its
// persisted commit, then the contiguous entries past it whose body and chain
// checksums verify and whose term does not decrease, cut at the first that
// fails, so no write the old cluster acknowledged is lost;
// a new cluster id that records its predecessor and the last index it kept;
// and a forced configuration. The audit file records how many entries past
// the persisted commit were adopted. Every old voter left out must be
// attested fenced by every witness; without witness keys it fails closed.
// A stop marker, STOP-RECOVER, is durable before the forced entry and
// removed once the new stamp is: a Recover interrupted between the two
// refuses a start, and is completed by running Recover again to the same
// new cluster, which resumes from the index the first one kept; one to
// another cluster is refused. A silence condition TruncateAndStaySilent
// left is removed before the marker: the new history is closed to the
// terms it guarded, and a silent node never campaigns. An install a crash
// interrupted after its snapshot was published is finished first, before
// the marker: the new history keeps that snapshot's index, and the forced
// entry follows it.
func Recover(d DataDir, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RecoverOptions) error {
	if err := d.offline(); err != nil {
		return err
	}
	flog := d.d.log
	if errs.HasCode(flog.refuse, coreconsensus.CodeStopMarkerPresent) {
		// A STORAGE_FAILED stop of this boot: reboot first (#563).
		return flog.refuse
	}
	if flog.refuse != nil {
		return recoverRefused("quarantined log")
	}
	stamp := flog.stamp
	intent, err := d.readRecoverIntent()
	if err != nil {
		return err
	}
	if intent != nil {
		if intent.cluster != newCluster {
			return recoverRefused("an interrupted Recover names cluster " + intent.cluster.String() + ": run it again to that cluster")
		}
		if stamp.Log.Cluster == newCluster {
			// Interrupted after its stamp: only the marker is left.
			return d.finishRecover(newCluster, opt)
		}
	}
	if err := checkRecover(&stamp, newCluster, opt); err != nil {
		return err
	}
	if err := checkRecoverMembers(&stamp, members); err != nil {
		return err
	}
	plan, err := d.planRecover(&stamp, newCluster, members, opt, intent)
	if err != nil {
		return err
	}
	return d.writeRecover(&stamp, newCluster, members, opt.Operator, &plan)
}

// interruptedInstall reports the install a crash interrupted after its
// snapshot was published — the install's commit point (D7) — and before the
// log was moved onto it: the newest snapshot that verifies, when it lies past
// the log's end or does not match the log's term at its index. The next
// start finishes it (finishInstall); Recover finishes it first and keeps it,
// or its forced entry followed the short log, the start erased it, and the
// node ran the snapshot's old voters and never led again (#506). It reads
// and writes nothing else.
func (d DataDir) interruptedInstall() (coreconsensus.SnapshotMetaValue, bool, error) {
	metas, err := d.d.snaps.List()
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, false, err
	}
	flog := d.d.log
	for _, m := range metas {
		if d.verifySnapshotFile(m) != nil {
			continue // a start falls back past it too (D7)
		}
		first, last := flog.Bounds()
		idx := m.Position.Index
		if idx < first {
			return m, false, nil
		}
		if idx <= last {
			if t, terr := flog.Term(idx); terr == nil && t == m.Position.Term {
				return m, false, nil
			}
		}
		return m, true, nil
	}
	return coreconsensus.SnapshotMetaValue{}, false, nil
}

// finishInterruptedInstall moves the log onto the snapshot of an
// interrupted install, durably, as finishInstall does at a start: the log is
// replaced by that snapshot's base.
func (d DataDir) finishInterruptedInstall(m *coreconsensus.SnapshotMetaValue) error {
	flog := d.d.log
	first, _ := flog.Bounds()
	if err := flog.TruncateSuffix(first); err != nil {
		return storageFailed("truncate", err)
	}
	if err := compactLog(flog, m.Position.Index, m.Position.Term); err != nil {
		return storageFailed("compact", err)
	}
	if err := flog.Sync(); err != nil {
		return storageFailed("sync", err)
	}
	return nil
}

// verifySnapshotFile reads a published snapshot back offline and checks its
// sizes and digest, with the codec its metadata names.
func (d DataDir) verifySnapshotFile(m coreconsensus.SnapshotMetaValue) (err error) {
	own, _ := defaultCodec()
	codec, err := snapshotCodec(&m, own)
	if err != nil {
		return err
	}
	h, err := d.d.snaps.Open(m.ID)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := h.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return verifySnapshot(io.NewSectionReader(h, 0, m.EncodedSize), m, codec, max(m.DecodedSize, m.EncodedSize))
}

// recoverIntent is what STOP-RECOVER records of the Recover that wrote it:
// the new cluster, and the last index of the old history it keeps.
type recoverIntent struct {
	cluster coreconsensus.ClusterID
	kept    uint64
}

// recoverPlan is what Recover decided before it writes the new history: the
// hard state to force from, the last index kept, the configuration in force
// there, and whether the marker is already durable.
type recoverPlan struct {
	hard    coreconsensus.HardState
	kept    uint64
	old     coreconsensus.MembershipValue
	resumed bool
	// install is the snapshot of an install a crash interrupted, which
	// Recover finishes before its marker and keeps (#506); nil when none.
	install *coreconsensus.SnapshotMetaValue
}

// planRecover decides what the new history keeps, checks the fence of every
// old voter left out against the configuration in force there, and audits
// the operation, before anything else is written. A first Recover keeps the
// verified suffix past the persisted commit; a resumed one keeps what the
// first decided, read from its marker. Its forced entry, durable or not, is
// never read as the old configuration, so the fences are asked for again.
func (d DataDir) planRecover(stamp *coreconsensus.StampValue, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, opt *RecoverOptions, intent *recoverIntent) (recoverPlan, error) {
	var p recoverPlan
	hard, err := d.d.log.HardState()
	if err != nil {
		return p, err
	}
	p.hard, p.resumed = hard, intent != nil
	if err := d.planKept(&p, intent); err != nil {
		return p, err
	}
	if p.install != nil {
		// The snapshot holds every entry kept; the log below it is
		// replaced, and its configuration entries are older.
		p.old = p.install.Membership
	} else if p.old, err = lastMembership(d.d.log, d.d.snaps, p.kept); err != nil {
		// The old configuration decides which voters must be attested
		// fenced: one that could not be read fails Recover closed, never
		// open. It is the configuration of the last entry kept: an adopted
		// change counts.
		return p, err
	}
	if err := checkFenced(p.old.Voters(), members, opt, stamp.Log.Cluster, newCluster); err != nil {
		return p, err
	}
	return p, d.audit(opt.Operator, recoverAuditLine(newCluster, &p))
}

// planKept decides the last index the new history keeps: the one a resumed
// Recover's marker recorded; the index of the snapshot an interrupted
// install published, which Recover finishes first (#506); else every entry
// this survivor holds whose chain verifies, not only those up to its
// persisted commit (issue #399).
func (d DataDir) planKept(p *recoverPlan, intent *recoverIntent) error {
	var err error
	if p.resumed {
		p.kept, err = d.resumedKept(intent)
		return err
	}
	m, interrupted, err := d.interruptedInstall()
	if err != nil {
		return err
	}
	if interrupted {
		p.install, p.kept = &m, m.Position.Index
		return nil
	}
	p.kept, err = d.adoptedEnd(p.hard.Commit)
	return err
}

// recoverAuditLine is Recover's audit line: the index kept, the persisted
// commit, and how many entries past that commit the new history adopts. A
// resumed Recover reports the index its marker kept, the forced entry it
// writes again replacing any the interrupted one left.
func recoverAuditLine(newCluster coreconsensus.ClusterID, p *recoverPlan) string {
	if p.resumed {
		return fmt.Sprintf("recover %s resumed kept %d", newCluster, p.kept)
	}
	return fmt.Sprintf("recover %s kept %d commit %d adopted %d", newCluster, p.kept, p.hard.Commit, p.kept-p.hard.Commit)
}

// resumedKept is the index an interrupted Recover kept, refused when the log
// no longer holds it: the forced entry follows it, and nothing else may.
func (d DataDir) resumedKept(intent *recoverIntent) (uint64, error) {
	if _, last := d.d.log.Bounds(); intent.kept > last {
		return 0, recoverRefused("the interrupted Recover kept an index past the log")
	}
	return intent.kept, nil
}

// writeRecover writes the plan: the marker, unless it is already durable,
// then the forced entry, the new stamp, and the marker's removal. Between
// the forced entry and the stamp, the directory holds the new members'
// configuration under the OLD cluster's identity: started then, the node
// would run it in the old cluster, beside voters only fenced for the new
// one. The marker refuses that start (#405).
func (d DataDir) writeRecover(stamp *coreconsensus.StampValue, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue, op coreconsensus.Operator, p *recoverPlan) error {
	if p.install != nil {
		// Before the marker: a crash after it reruns as a first Recover,
		// over a log whose base is the snapshot, and keeps the same index.
		if err := d.finishInterruptedInstall(p.install); err != nil {
			return err
		}
	}
	if !p.resumed {
		if err := d.writeRecoverIntent(&recoverIntent{cluster: newCluster, kept: p.kept}); err != nil {
			return err
		}
	}
	if err := d.appendForced(new(coreconsensus.HardState{Term: p.hard.Term, Commit: p.kept}), members, &p.old, op); err != nil {
		return err
	}
	stamp.Log = coreconsensus.LogIdentityValue{
		Cluster: newCluster, Epoch: 1, Predecessor: stamp.Log.Cluster,
		PredecessorEpoch: stamp.Log.Epoch, KeptIndex: p.kept,
	}
	if err := d.d.log.WriteStamp(*stamp); err != nil {
		return err
	}
	if err := d.endRecoveredSilence(); err != nil {
		return err
	}
	return d.clearRecoverIntent()
}

// endRecoveredSilence removes, durably and before STOP-RECOVER goes, the
// silence condition a TruncateAndStaySilent left: the new history is closed
// to the old terms it guarded, and a silent node never campaigns, so a
// survivor kept silent would be the one voter of its history and never be
// elected (#524). A crash before it is completed by the rerun, which finds
// the new stamp and goes through finishRecover.
func (d DataDir) endRecoveredSilence() error {
	if err := d.d.log.endSilence(); err != nil {
		return storageFailed("silence condition", err)
	}
	return nil
}

// finishRecover completes a Recover interrupted after its new stamp, of
// which only the marker is left: confirmed and audited like the rest, then
// the marker is removed. Nothing else is decided again.
func (d DataDir) finishRecover(newCluster coreconsensus.ClusterID, opt *RecoverOptions) error {
	switch {
	case !opt.Operator.Valid():
		return recoverRefused("operator")
	case opt.Confirm != newCluster.String():
		return recoverRefused("confirmation does not name the new cluster")
	}
	if err := d.audit(opt.Operator, fmt.Sprintf("recover %s completed", newCluster)); err != nil {
		return err
	}
	if err := d.endRecoveredSilence(); err != nil {
		return err
	}
	return d.clearRecoverIntent()
}

// writeRecoverIntent publishes STOP-RECOVER, durably. Its first two lines
// are what Recover reads back; the third is what a refused start shows the
// operator, as the detail of STOP_MARKER_PRESENT.
func (d DataDir) writeRecoverIntent(in *recoverIntent) error {
	body := fmt.Sprintf("cluster=%s\nkept=%d\nRecover to cluster %s was interrupted: run Recover again to cluster %s to complete it\n",
		in.cluster, in.kept, in.cluster, in.cluster)
	if err := writeFileSync(filepath.Join(d.d.dir, markerRecover), []byte(body)); err != nil {
		return storageFailed("recover marker", err)
	}
	return nil
}

// readRecoverIntent reads STOP-RECOVER: nil when there is none. A marker that
// cannot be read is a STORAGE_FAILED, and one that does not parse refuses
// Recover: neither is read as no Recover in progress.
func (d DataDir) readRecoverIntent() (*recoverIntent, error) {
	b, err := os.ReadFile(filepath.Join(d.d.dir, markerRecover))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, storageFailed("recover marker", err)
	}
	var cluster string
	in := &recoverIntent{}
	if _, err := fmt.Sscanf(string(b), "cluster=%s\nkept=%d\n", &cluster, &in.kept); err != nil {
		return nil, recoverRefused("unreadable " + markerRecover + " marker")
	}
	raw, err := hex.DecodeString(cluster)
	if err != nil || len(raw) != len(in.cluster) {
		return nil, recoverRefused("unreadable " + markerRecover + " marker")
	}
	copy(in.cluster[:], raw)
	return in, nil
}

// clearRecoverIntent removes STOP-RECOVER once the new stamp is durable,
// and syncs the directory, so the removal is as durable as the stamp.
func (d DataDir) clearRecoverIntent() error {
	if err := os.Remove(filepath.Join(d.d.dir, markerRecover)); err != nil {
		return storageFailed("recover marker", err)
	}
	if err := syncDir(d.d.dir); err != nil {
		return storageFailed("recover marker", err)
	}
	return nil
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
	if commit+1 < first {
		return d.adoptedPastTheBase(first, last)
	}
	if last <= commit {
		return commit, nil
	}
	chain, term, err := d.chainAt(commit, first)
	if err != nil {
		return 0, err
	}
	return d.verifiedEnd(commit, last, term, chain)
}

// adoptedPastTheBase is adoptedEnd over a log compacted past its persisted
// commit: a snapshot covers only applied, hence committed, entries, so the
// base first-1 is committed. The kept index used to stay at the persisted
// commit, below the log: the forced entry could not be appended there, and
// every rerun failed the same way (#422). The suffix is verified from the
// base when a snapshot ends there, else from the log's first entry, whose
// body is verified and whose link to the compacted entry before it no
// store can check. With neither, nothing the forced entry could chain to is
// known: Recover refuses, and says why.
func (d DataDir) adoptedPastTheBase(first, last uint64) (uint64, error) {
	metas, err := d.d.snaps.List()
	if err != nil {
		return 0, err
	}
	if m, ok := snapshotAt(metas, first-1); ok {
		return d.verifiedEnd(first-1, last, m.Position.Term, m.ChainTip)
	}
	if last < first {
		return 0, recoverRefused("the log is compacted past its persisted commit, and nothing records the chain at its base")
	}
	es, err := d.d.log.Entries(first, first+1, 1, nil)
	if err != nil {
		return 0, err
	}
	e := &es[0]
	if wire.BodySum(e.Data) != e.BodySum {
		return 0, recoverRefused("the log is compacted past its persisted commit, and its first entry does not verify")
	}
	return d.verifiedEnd(first, last, e.Position.Term, e.ChainSum)
}

// verifiedEnd walks the log from end, the entry of term term and chain sum
// chain, and returns the last index whose link verifies.
func (d DataDir) verifiedEnd(end, last, term uint64, chain uint32) (uint64, error) {
	flog := d.d.log
	var buf []coreconsensus.EntryValue
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
	if m, ok := snapshotAt(metas, commit); ok {
		return m.ChainTip, m.Position.Term, nil
	}
	return 0, 0, nil
}

// snapshotAt is the snapshot of metas that ends at index: the newest is not
// always the one, when it failed its checksums and the base is the older's.
func snapshotAt(metas []coreconsensus.SnapshotMetaValue, index uint64) (coreconsensus.SnapshotMetaValue, bool) {
	for _, m := range metas {
		if m.Position.Index == index {
			return m, true
		}
	}
	return coreconsensus.SnapshotMetaValue{}, false
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
	// Codec decodes the chunks; nil is the codec Expected names, flate
	// when it names none.
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
	if errs.HasCode(flog.refuse, coreconsensus.CodeStopMarkerPresent) {
		// A STORAGE_FAILED stop of this boot: reboot first (#563).
		return flog.refuse
	}
	if err := checkRestoreTarget(flog, newCluster, opt); err != nil {
		return err
	}
	if err := checkRestoreBounds(flog, stamp, opt); err != nil {
		return err
	}
	return checkRestoreMembers(stamp, members)
}

// restoreCodec is the codec the restored chunks are decoded with: the
// caller's, else the one the expected metadata names, else the default,
// flate. Read with flate whatever the metadata said, a snapshot a node cut
// with another codec failed as corrupt instead of being refused cleanly
// when this build does not link its codec (D18, #548).
func restoreCodec(opt *RestoreOptions) (coretransform.BoundedDecompressor, error) {
	if opt.Codec != nil {
		return opt.Codec, nil
	}
	if opt.Expected.Codec != "" {
		return linkedCodec(opt.Expected.Codec)
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
//
// The term is read before anything is changed and persisted before the
// truncation, taken at least as high as a condition a crashed run already
// persisted: a rerun after a crash past the truncation reads a log whose
// dropped records no longer name their term, and a lower term would let the
// node vote again in the term of a vote the truncation dropped (#418).
func (d DataDir) truncateAndStaySilent() error {
	segN, off, err := d.quarantinePoint()
	if err != nil {
		return err
	}
	logDir := filepath.Join(d.d.dir, "log")
	maxTerm, err := logMaxTerm(logDir)
	if err != nil {
		return err
	}
	prev, err := persistedSilenceTerm(d.d.dir)
	if err != nil {
		return err
	}
	maxTerm = max(maxTerm, prev)
	// The condition is durable BEFORE the truncation and the marker go: a
	// crash in between leaves a silent node, never a voting one. Its index
	// is filled in once the truncated log is read (0 means "the end of the
	// log").
	if err := writeFileSync(filepath.Join(d.d.dir, fileSilent), silenceRecord(maxTerm, 0)); err != nil {
		return storageFailed("silence condition", err)
	}
	if err := truncateAtDamage(logDir, segN, off); err != nil {
		return err
	}
	if err := syncDir(logDir); err != nil {
		return storageFailed("truncate", err)
	}
	if d.d.afterTruncate != nil {
		if err := d.d.afterTruncate(); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(d.d.dir, markerQuarantine)); err != nil {
		return storageFailed("clear marker", err)
	}
	return d.reopenSilenced(maxTerm)
}

// persistedSilenceTerm is the term of the silence condition already in dir,
// 0 when there is none. One that cannot be read refuses the exit: it is
// never overwritten with a term that may be lower.
func persistedSilenceTerm(dir string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileSilent))
	switch {
	case os.IsNotExist(err):
		return 0, nil
	case err != nil:
		return 0, storageFailed("silence condition", err)
	case len(b) != 16:
		return 0, storageFailed("silence condition", errSilenceLength)
	}
	return binary.LittleEndian.Uint64(b), nil
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

// logSegments lists the published segments of logDir by number; a name
// that is not exactly a segment's is ignored.
func logSegments(logDir string) (map[uint64]string, error) {
	ents, err := os.ReadDir(logDir)
	if err != nil {
		return nil, storageFailed("readdir", err)
	}
	segs := map[uint64]string{}
	for _, e := range ents {
		var n uint64
		if _, err := fmt.Sscanf(e.Name(), "seg-%016x", &n); err != nil || e.Name() != segName(n) {
			continue
		}
		segs[n] = filepath.Join(logDir, e.Name())
	}
	return segs, nil
}

// logMaxTerm is the highest term of every readable record of the log,
// before and after any damage. It changes nothing.
func logMaxTerm(logDir string) (uint64, error) {
	segs, err := logSegments(logDir)
	if err != nil {
		return 0, err
	}
	var maxTerm uint64
	for _, path := range segs {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, storageFailed("read segment", err)
		}
		maxTerm = max(maxTerm, maxRecordTerm(b))
	}
	return maxTerm, nil
}

// truncateAtDamage deletes every segment after segment segN, and segN too
// when the damage is at its start, and zeroes segN from off otherwise.
func truncateAtDamage(logDir string, segN uint64, off int64) error {
	segs, err := logSegments(logDir)
	if err != nil {
		return err
	}
	for n, path := range segs {
		st, err := os.Stat(path)
		if err != nil {
			return storageFailed("read segment", err)
		}
		if err := truncateSegment(path, n, segN, off, st.Size()); err != nil {
			return err
		}
	}
	return nil
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
