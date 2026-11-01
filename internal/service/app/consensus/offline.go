package consensus

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

const (
	// WipeAndRejoin wipes the directory; the operator then Removes the old
	// NodeID and Inits under a NEW one (R4-03). A voter's incarnation is
	// never swapped in place. A wipe that does not finish leaves the stop
	// marker STOP-WIPE, which refuses a start, Init, Redraw, Recover,
	// RestoreSnapshot and TruncateAndStaySilent until WipeAndRejoin runs
	// again and completes it.
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
	// markerWipe is the stop marker of a WipeAndRejoin in progress: written,
	// synced, before anything is removed, and removed last, once the stores,
	// then the stamp and every other marker, are durably gone. A wipe a crash
	// interrupted left a member stamp over an empty log, and the node
	// restarted as the same voter with no term, no vote and no entry (#690).
	// While it is there a start is refused, the log is not recovered at
	// open, every offline write but the wipe refuses, and the wipe run again
	// completes.
	markerWipe = markerStopPrefix + "WIPE"
	// wipeDetail is what STOP-WIPE shows the operator, as the detail of
	// STOP_MARKER_PRESENT.
	wipeDetail = "WipeAndRejoin was interrupted: run ClearQuarantine(WipeAndRejoin) again to complete it, then Init under a new NodeID\n"
	// whyOldMember is the reason checkOldMembers refuses a Recover.
	whyOldMember = "a member other than this node carries the ID of an old member: the others are nodes Init prepared under the new cluster"
	// whyRepeatedID is the reason Recover and RestoreSnapshot refuse two
	// members under one NodeID.
	whyRepeatedID = "two members under one id"
)

// offlineBatchBytes and offlineBatchEntries bound one read of the log by an
// offline operation: the payload bytes and the entries of a batch, which the
// next read reuses. Recover read the survivor's whole retained log in one
// call, its payloads included, three times over (#700). A batch holds 1 MiB
// of payloads, the default MaxBatchBytes, and 4096 entries, about 224 KiB
// of EntryValue however small their payloads; an entry larger than the
// bytes is read alone.
const (
	offlineBatchBytes   int    = 1 << 20
	offlineBatchEntries uint64 = 4096
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

// offlineWrite is offline, and refuses as well a directory whose wipe was
// interrupted: what it holds is neither the old member nor an empty
// directory, and only WipeAndRejoin, run again, may write there (#690).
func (d DataDir) offlineWrite() error {
	if err := d.offline(); err != nil {
		return err
	}
	return wipeMarker(d.d.dir)
}

// wipeMarker is the STOP_MARKER_PRESENT of the STOP-WIPE marker of dir, nil
// when there is none. One that is there but cannot be read is a
// STORAGE_FAILED, never read as no wipe in progress.
func wipeMarker(dir string) error {
	p := filepath.Join(dir, markerWipe)
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return storageFailed("read stop marker", err)
	}
	return stopMarkerPresent(p, b)
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
	// A half-wiped directory still holds the old member stamp, which the
	// idempotent path would hand back as the "new" pair (#690).
	if err := d.offlineWrite(); err != nil {
		return 0, coreconsensus.Incarnation{}, err
	}
	return InitLog(d.Log(), cluster, slot)
}

// Redraw draws a new id, only while the stamp is joining and the log empty,
// after a JOIN_REFUSED for a reused or retired id (R8-10).
func Redraw(d DataDir) (coreconsensus.NodeID, error) {
	if err := d.offlineWrite(); err != nil {
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
	// Last is the last index the directory holds as the start and Recover
	// keep it, the half of the survivor's key LastTerm completes: the log's
	// last index, or the index of the snapshot an install a crash interrupted
	// published, which they move the log onto, whether it lies past the log's
	// end or inside the log under another term (the log's entries past it
	// are then dropped). It is not the log's bound after a Recover, which
	// may still drop a suffix that does not verify, and appends its forced
	// entry after the index it keeps.
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
	if l.refuse == nil {
		if r.Last, r.LastTerm, err = d.keptLast(r.Snapshots); err != nil {
			return ReportValue{}, err
		}
	}
	if r.Markers, err = stopMarkers(d.d.dir); err != nil {
		return ReportValue{}, err
	}
	return r, nil
}

// keptLast is the survivor's key of the directory, its last index and the
// term there, as the start (finishInstall) and Recover (interruptedInstall)
// keep them. The term is the half of the key Inspect did not report at
// first: two survivors may show the same last index and the same
// HardState.Term, the term of a vote, and only the term of their last
// entries tells which one holds the writes a majority committed (D17,
// #534).
//
// An install a crash interrupted after its snapshot was published holds
// the state through that snapshot, and the start and Recover move the log
// onto it: past the log's end (#506), or inside the log under another
// term, the log's entries past it dropped. Reported by the log's tail, such
// a survivor ranked by entries it no longer holds once started, the rule
// chose another survivor whose stale tail was as long, and Recover there
// lost the acknowledged writes this one held (#746). The snapshot is the
// one Recover finishes, the newest that verifies: a newest one that fails
// its checksums counts no more than it does there, and one of a version
// this build does not read refuses the report, as it refuses Recover and
// the start (#640). A newest snapshot below the log's first index, or that
// matches the log's term at its index, is no interrupted install's whatever
// its checksums, and is not read: forgejo inspects its directory at every
// start. A log with no entry and no snapshot reports 0.
func (d DataDir) keptLast(metas []coreconsensus.SnapshotMetaValue) (last, term uint64, err error) {
	l := d.d.log
	if len(metas) > 0 && unfinishedInstall(l, &metas[0]) {
		m, interrupted, ierr := d.interruptedInstall()
		switch {
		case ierr != nil:
			return 0, 0, ierr
		case interrupted:
			return m.Position.Index, m.Position.Term, nil
		}
	}
	_, last = l.Bounds()
	if t, terr := l.Term(last); terr == nil {
		term = t
	}
	return last, term, nil
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
		if err := wipeMarker(d.d.dir); err != nil {
			// The log a wipe began to delete is not one to truncate: the
			// wipe is completed, never undone (#690).
			return err
		}
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

// wipeAndRejoin is the WipeAndRejoin exit, in an order a crash at any step
// leaves safe (#690): audited first; then STOP-WIPE, durable before
// anything is removed; the log closed, its stores deleted and the
// directory synced; the stamp, the quarantine marker, the silence condition
// and every other stop marker deleted and the directory synced; STOP-WIPE
// removed last and the directory synced; and the directory reopened empty.
// Interrupted anywhere before the last step, the directory keeps STOP-WIPE,
// which refuses a start and every offline write, and the wipe run again
// resumes it: it keeps nothing, so it starts over. It is never undone.
func (d DataDir) wipeAndRejoin(op coreconsensus.Operator) error {
	if err := d.audit(op, "clear-quarantine wipe-and-rejoin"); err != nil {
		return err
	}
	if err := d.markWipe(); err != nil {
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
	if err := d.clearWipeMarker(); err != nil {
		return err
	}
	return d.d.open()
}

// markWipe publishes STOP-WIPE, durably, before the wipe removes anything.
func (d DataDir) markWipe() error {
	if err := writeFileSync(filepath.Join(d.d.dir, markerWipe), []byte(wipeDetail)); err != nil {
		return storageFailed("wipe marker", err)
	}
	return d.wipeStep("marker")
}

// clearWipeMarker removes STOP-WIPE, durably, once everything else the wipe
// removes is durably gone: until then nothing tells a directory this wipe
// has not finished from the old member's.
func (d DataDir) clearWipeMarker() error {
	if err := os.Remove(filepath.Join(d.d.dir, markerWipe)); err != nil {
		return storageFailed("wipe marker", err)
	}
	if err := syncDir(d.d.dir); err != nil {
		return storageFailed("wipe", err)
	}
	return d.wipeStep("done")
}

// wipeStep runs the test hook after a step of the wipe, nil when no test
// set it.
func (d DataDir) wipeStep(step string) error {
	if d.d.afterWipeStep == nil {
		return nil
	}
	return d.d.afterWipeStep(step)
}

// removeStores deletes the log and the snapshots, whole, then syncs the
// directory: the stamp goes only once their removal is durable, and with
// the stamp the last trace of the old member but STOP-WIPE.
func (d DataDir) removeStores() error {
	for _, name := range []string{"log", "snap"} {
		if err := os.RemoveAll(filepath.Join(d.d.dir, name)); err != nil {
			return storageFailed("wipe", err)
		}
		if err := d.wipeStep(name); err != nil {
			return err
		}
	}
	if err := syncDir(d.d.dir); err != nil {
		return storageFailed("wipe", err)
	}
	return nil
}

// removeStampAndMarkers deletes the stamp, the quarantine marker, the
// silence condition and every stop marker but STOP-WIPE, which goes last,
// and the boot-scoped STORAGE_FAILED marker, then syncs the directory. A
// directory that could not be listed keeps its stamp and its markers: the
// wipe fails here rather than reopen a directory it did not clear.
func (d DataDir) removeStampAndMarkers() error {
	ents, err := os.ReadDir(d.d.dir)
	if err != nil {
		return storageFailed("wipe", err)
	}
	for _, e := range ents {
		if wipedWithStamp(e.Name()) {
			if err := os.Remove(filepath.Join(d.d.dir, e.Name())); err != nil {
				return storageFailed("wipe", err)
			}
		}
	}
	if err := d.removeBootMarker(); err != nil {
		return err
	}
	if err := syncDir(d.d.dir); err != nil {
		return storageFailed("wipe", err)
	}
	return d.wipeStep("stamp")
}

// wipedWithStamp reports whether the wipe removes the data directory's
// entry name with the stamp: the quarantine marker, the silence condition
// — the new member must not start silent at the old log's index (#422) —
// and every stop marker but STOP-WIPE.
func wipedWithStamp(name string) bool {
	switch name {
	case fileStamp, markerQuarantine, fileSilent:
		return true
	case markerWipe:
		return false
	}
	return strings.HasPrefix(name, markerStopPrefix)
}

// removeBootMarker removes the boot-scoped STORAGE_FAILED marker, which
// exists only after a storage failure of this boot; one that cannot be
// removed would refuse the next start anyway, so the wipe says so now.
func (d DataDir) removeBootMarker() error {
	if err := os.Remove(d.d.log.runtimeMarker("STORAGE_FAILED")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return storageFailed("wipe", err)
	}
	return nil
}

// RecoverOptions are Recover's preconditions (R2-25, R3-02, R4-04, R9-08).
// Recover refuses, before it writes anything, an invalid operator, a
// confirmation that does not name the new cluster, no witness key, an old
// voter other than this node without a receipt from every witness, and
// another member under the ID of an old or retired member.
type RecoverOptions struct {
	// Confirm must equal the new cluster id in hex: a typed confirmation.
	Confirm string
	// Operator is recorded in the audit file.
	Operator coreconsensus.Operator
	// Fenced attests every old voter but this node, whether the new members list it or not.
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
// the persisted commit were adopted. Every old voter but this node must be
// attested fenced by every witness, whatever the new members name, and no
// other member may carry the ID of an old or retired member, or of another
// member; without witness keys it fails closed.
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
	if err := d.offlineWrite(); err != nil {
		return err
	}
	flog := d.d.log
	if errs.HasCode(flog.refuse, coreconsensus.CodeStopMarkerPresent) {
		// A STORAGE_FAILED stop of this boot: reboot first (#563); or a
		// wipe interrupted: complete it (#690).
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
// node ran the snapshot's old voters and never led again (#506). One of a
// version, codec or cluster version this build does not know refuses it
// VERSION_UNSUPPORTED, as it refuses a start: unknown means stop, never
// skip (D18). Skipped as one that fails its checksums, a snapshot above
// ProtocolMax let Recover plan from the older log below it and write a new
// history from a state older than the one the directory holds (#640). One
// a cut before #544 published at term 0 is set aside unread, as the start
// sets it aside (restoreLatest): taken for an install, it dropped the log
// for a position with no term, which the compaction then refused, and
// Inspect ranked the survivor at term 0. One this node's disk cannot read,
// a STORAGE_FAILED, refuses too: only a snapshot's own fault sets it aside.
// Set aside, it let Recover plan from the short log below it, while a start
// keeps such a snapshot published (ownFailure) and restores it as soon as
// the disk reads it again, dropping the log past it: the forced entry and
// every write the new history acknowledged (#789). The operator runs it
// again once the disk reads. A SNAPSHOT_TOO_LARGE, which a start keeps as
// a limit of its configuration, is the snapshot's own fault here: the bound
// is its own metadata's, and a stream past it never restores. It reads and
// writes nothing else.
func (d DataDir) interruptedInstall() (coreconsensus.SnapshotMetaValue, bool, error) {
	metas, err := d.d.snaps.List()
	if err != nil {
		return coreconsensus.SnapshotMetaValue{}, false, err
	}
	for _, m := range metas {
		if termless(&m) {
			continue // the start sets it aside too (#544)
		}
		if verr := d.verifySnapshotFile(m); verr != nil {
			if errs.HasCode(verr, coreconsensus.CodeVersionUnsupported) || errs.HasCode(verr, coreconsensus.CodeStorageFailed) {
				return coreconsensus.SnapshotMetaValue{}, false, verr
			}
			continue // a start falls back past it too (D7)
		}
		return m, unfinishedInstall(d.d.log, &m), nil
	}
	return coreconsensus.SnapshotMetaValue{}, false, nil
}

// unfinishedInstall reports whether the published snapshot m is one an
// install a crash interrupted left over l, the rule finishInstall applies
// at a start: it lies at or past the log's first index, and past the log's
// end or under another term than the log's at its index. It reads no
// snapshot.
func unfinishedInstall(l *fileLog, m *coreconsensus.SnapshotMetaValue) bool {
	first, last := l.Bounds()
	idx := m.Position.Index
	switch {
	case idx < first:
		return false
	case idx <= last:
		t, err := l.Term(idx)
		return err != nil || t != m.Position.Term
	}
	return true
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
	hard coreconsensus.HardState
	kept uint64
	// adopted is the last kept index the old history had committed, the
	// new identity's AdoptedFrom; 0 records none (#509).
	adopted uint64
	old     coreconsensus.MembershipValue
	// cluster is the cluster version the history leaves at kept, which the
	// forced entry's payload version follows (#648): read with old, in the
	// same pass over the log, and handed down rather than read again (#700).
	// An interrupted install's is read once the install is finished.
	cluster uint32
	resumed bool
	// install is the snapshot of an install a crash interrupted, which
	// Recover finishes before its marker and keeps (#506); nil when none.
	install *coreconsensus.SnapshotMetaValue
}

// planRecover decides what the new history keeps, checks against the
// configuration in force there that no other member carries an old ID and
// that every old voter but this node is fenced, and audits the operation,
// before anything else is written. A first Recover keeps the
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
	} else if p.old, p.cluster, err = inForceAt(d.d.log, d.d.snaps, p.kept); err != nil {
		// The old configuration decides which voters must be attested
		// fenced: one that could not be read fails Recover closed, never
		// open. It is the configuration of the last entry kept: an adopted
		// change counts.
		return p, err
	}
	if err := checkOldMembers(&p.old, members, stamp.ID); err != nil {
		return p, err
	}
	if err := checkFenced(p.old.Voters(), stamp.ID, opt, stamp.Log.Cluster, newCluster); err != nil {
		return p, err
	}
	d.planAdopted(&p)
	return p, d.audit(opt.Operator, recoverAuditLine(newCluster, &p))
}

// planAdopted decides the new identity's AdoptedFrom: the index up to
// which the kept entries were committed in the old history, the persisted
// commit or the base of a compacted log, all of the snapshot an
// interrupted install published. A resumed Recover's marker does not
// record it, and records none. It is recorded only when the history kept
// is at protocol version 2: the stamp that carries it is one a node of
// protocol 1 refuses to open (#509, D18). The cluster version is the one
// planRecover read with the old configuration.
func (d DataDir) planAdopted(p *recoverPlan) {
	var cluster uint32
	switch {
	case p.resumed:
		return
	case p.install != nil:
		p.adopted, cluster = p.kept, p.install.ClusterVersion
	default:
		committed := p.hard.Commit
		if first, _ := d.d.log.Bounds(); first > 0 && first-1 > committed {
			committed = first - 1
		}
		p.adopted, cluster = min(p.kept, committed), p.cluster
	}
	if cluster < raft.Protocol2 {
		p.adopted = 0
	}
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
		if err := d.finishPlannedInstall(p); err != nil {
			return err
		}
	}
	if !p.resumed {
		if err := d.writeRecoverIntent(&recoverIntent{cluster: newCluster, kept: p.kept}); err != nil {
			return err
		}
	}
	if err := d.appendForced(new(coreconsensus.HardState{Term: p.hard.Term, Commit: p.kept}), members, &p.old, p.cluster, op); err != nil {
		return err
	}
	stamp.Log = coreconsensus.LogIdentityValue{
		Cluster: newCluster, Epoch: 1, Predecessor: stamp.Log.Cluster,
		PredecessorEpoch: stamp.Log.Epoch, KeptIndex: p.kept, AdoptedFrom: p.adopted,
	}
	if err := d.d.log.WriteStamp(*stamp); err != nil {
		return err
	}
	if err := d.endRecoveredSilence(); err != nil {
		return err
	}
	return d.clearRecoverIntent()
}

// finishPlannedInstall finishes the install a crash interrupted, which the
// plan keeps, then reads the cluster version the forced entry follows: the
// log now starts past the snapshot, so nothing of it is read (#700).
func (d DataDir) finishPlannedInstall(p *recoverPlan) error {
	if err := d.finishInterruptedInstall(p.install); err != nil {
		return err
	}
	var err error
	_, p.cluster, err = inForceAt(d.d.log, d.d.snaps, p.kept)
	return err
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

// checkRecoverMembers refuses new members that are not all pinned, among
// which this node, at its incarnation, is not a voter, which name another
// voter, or two members under one ID. The others enter as learners and are promoted through
// Promote, never as empty voters (R3-02, R4-04): Promote waits for a
// learner's acknowledgement of the configuration that admitted it, which
// it judged against the commit alone before #722, and a member Init only
// prepared cannot vote before a leader admits it, so a forced configuration
// that needed its vote never elected one, and two survivors naming each
// other recovered two histories into one identity (#642). Named learners,
// the two survivors still did; the old configuration, which refuses them,
// is read later (checkOldMembers, #691).
func checkRecoverMembers(stamp *coreconsensus.StampValue, members []coreconsensus.MemberValue) error {
	self := false
	for _, m := range members {
		if isZeroDigest(m.SPKI) || m.ID == 0 {
			return recoverRefused("a member without an SPKI pin")
		}
		if m.ID != stamp.ID && m.Voter {
			return recoverRefused("a voter other than this node: the other members enter as learners")
		}
		if m.ID == stamp.ID && m.Voter {
			self = m.Incarnation == stamp.Incarnation
		}
	}
	if sharedID(members) {
		return recoverRefused(whyRepeatedID)
	}
	if sharedKey(members) {
		return recoverRefused("two members under one key")
	}
	if !self {
		return recoverRefused("this node is not a voter of the new members")
	}
	return nil
}

// checkOldMembers refuses new members that name, beside this node, an ID
// the old configuration lists, voter or learner, or retired. The others
// are joiners Init prepared under the new cluster id, each under a NEW
// NodeID (R1-17, R3-02, R4-04): an old member's ID is a node of the old
// cluster, and it reaches the new one only by a Recover of its own, a
// second history under the same identity. Two survivors that each named
// the other a learner were both accepted, with no fence for the other, and
// ran two leaders at one term (#691).
func checkOldMembers(old *coreconsensus.MembershipValue, members []coreconsensus.MemberValue, self coreconsensus.NodeID) error {
	for _, m := range members {
		if m.ID == self {
			continue
		}
		if _, ok := old.Member(m.ID); ok || old.IsRetired(m.ID) {
			return recoverRefused(whyOldMember)
		}
	}
	return nil
}

// checkFenced refuses a Recover unless every old voter but this node holds
// a fence receipt from EVERY witness, whatever the new members name: the
// ADR asks one attestation per old voter (R4-04, R9-08), and an old voter
// excused as a member let two survivors recover two histories into one
// identity (#691).
func checkFenced(voters []coreconsensus.NodeID, self coreconsensus.NodeID, opt *RecoverOptions, oldCluster, newCluster coreconsensus.ClusterID) error {
	for _, v := range voters {
		if v == self {
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
// knew. It keeps the old configuration's retired IDs and, in a history at
// protocol version 3, its retired keys, in an entry of payload version 3:
// below, the entry is of version 0, as it always was (#648). cluster is the
// cluster version the history leaves at hard.Commit, which Recover read
// with old (#700).
func (d DataDir) appendForced(hard *coreconsensus.HardState, members []coreconsensus.MemberValue, old *coreconsensus.MembershipValue, cluster uint32, operator coreconsensus.Operator) error {
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
	var version uint32
	if cluster >= raft.Protocol3 {
		version, conf.RetiredKeys = wire.AdminVersion3, slices.Clone(old.RetiredKeys)
	}
	data := wire.AppendAdminAt(nil, version, &wire.AdminValue{Operator: string(operator), Change: wire.ChangeForced, Membership: conf})
	e := coreconsensus.EntryValue{
		Position: coreconsensus.PositionValue{Term: term, Index: commit + 1}, Kind: coreconsensus.EntryMembership,
		Version: version, BodySum: wire.BodySum(data), Data: data,
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

// scanLog hands f every entry of l from lo through hi, in order, read a
// batch at a time into one slice the next batch reuses: what it holds does
// not grow with the log (#700). An entry the log cannot give back fails it,
// as it failed the whole read.
func scanLog(l *fileLog, lo, hi uint64, f func(e *coreconsensus.EntryValue)) error {
	var buf []coreconsensus.EntryValue
	for next := lo; next <= hi; next += uint64(len(buf)) {
		es, err := l.Entries(next, min(hi+1, next+offlineBatchEntries), offlineBatchBytes, buf[:0])
		if err != nil {
			return err
		}
		if !contiguousFrom(es, next) {
			return storageFailed("entries", errImpossible)
		}
		for i := range es {
			f(&es[i])
		}
		// The batch's payloads go before the next is read, not as the next
		// read overwrites its slots one by one.
		clear(es)
		buf = es
	}
	return nil
}

// inForceAt is what the committed history leaves in force at index: the
// configuration, and the cluster version its raises leave, applied as the
// engine applies them at commit. Both start from the newest snapshot's
// metadata and follow the entries the log holds through index, a batch at
// a time (scanLog); a raise counts past the snapshot only. When the
// snapshot ends at or below index and the log holds its index under its
// term, the entries up to it are the history the snapshot was cut from,
// whose configuration its metadata states (raft's ConfAt, D7): they are not
// read, and a start runs the same configuration from the same base.
// Otherwise the log is read from its first entry, as it always was: for an
// index below the snapshot, and for a snapshot the log does not continue,
// an install a crash interrupted whose snapshot no longer verifies. The
// configuration used to be read from the log's first entry whatever the
// snapshot, and both in one read of the whole range: Recover held the
// survivor's whole retained log, payloads included, once for the
// configuration and twice for the cluster version (#700). A store that
// cannot answer fails it: a configuration read from half the sources is an
// older one, and Recover would ask for the fence of the wrong voters.
func inForceAt(l *fileLog, snaps *fileSnapshots, index uint64) (coreconsensus.MembershipValue, uint32, error) {
	metas, err := snaps.List()
	if err != nil {
		return coreconsensus.MembershipValue{}, 0, err
	}
	var h inForce
	if len(metas) > 0 {
		m := &metas[0]
		h = inForce{conf: m.Membership, ver: m.ClusterVersion, gen: m.ClusterGeneration, base: m.Position.Index}
	}
	first, last := l.Bounds()
	hi, lo := min(last, index), first
	if h.base >= first && h.base <= hi {
		if t, terr := l.Term(h.base); terr == nil && t == metas[0].Position.Term {
			lo = h.base + 1
		}
	}
	if lo > hi {
		return h.conf, h.ver, nil
	}
	if err := scanLog(l, lo, hi, h.add); err != nil {
		return coreconsensus.MembershipValue{}, 0, err
	}
	return h.conf, h.ver, nil
}

// inForce is what inForceAt reads in force: the configuration, the cluster
// version and its generation, from the snapshot at base on.
type inForce struct {
	conf      coreconsensus.MembershipValue
	ver       uint32
	gen, base uint64
}

// add applies e to what is in force: a configuration entry replaces the
// configuration, and a raise past the snapshot, of the current generation,
// to a higher version raises the cluster version. A payload that does not
// parse changes nothing.
func (h *inForce) add(e *coreconsensus.EntryValue) {
	switch {
	case e.Kind == coreconsensus.EntryMembership:
		if a, err := wire.ParseAdminAt(e.Version, e.Data); err == nil {
			h.conf = a.Membership
		}
	case e.Kind == coreconsensus.EntryClusterVersion && e.Position.Index > h.base:
		if a, err := wire.ParseAdminAt(e.Version, e.Data); err == nil && a.Pre.Generation == h.gen && a.ClusterVersion > h.ver {
			h.ver, h.gen = a.ClusterVersion, h.gen+1
		}
	}
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
	if err := d.offlineWrite(); err != nil {
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
	// A snapshot at a cluster version this build does not speak would found
	// a history at it: RestoreSnapshot, OpenSnapshot and ImportObserved all
	// come here first (#640).
	if err := knownClusterVersion(&opt.Expected); err != nil {
		return nil, err
	}
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
// incarnation, is not a voter, of which one is not pinned, or of which two
// carry one ID.
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
	if sharedID(members) {
		return restoreRefused(whyRepeatedID)
	}
	if sharedKey(members) {
		return restoreRefused("two members under one key")
	}
	return nil
}

// restoredMeta is the metadata the restored snapshot is published under:
// the expected one, in a new history that records the old cluster as its
// predecessor and the snapshot's index as the last it kept, with the new
// members, the retired IDs and the retired keys. Every entry a snapshot
// covers was committed: at protocol version 2 the identity records the
// whole kept range as adopted from a commit, in the stamp the restore
// writes; the meta does not carry it (#509).
func restoredMeta(exp *coreconsensus.SnapshotMetaValue, newCluster coreconsensus.ClusterID, members []coreconsensus.MemberValue) coreconsensus.SnapshotMetaValue {
	meta := *exp
	meta.Log = coreconsensus.LogIdentityValue{
		Cluster: newCluster, Epoch: 1, Predecessor: exp.Log.Cluster,
		PredecessorEpoch: exp.Log.Epoch, KeptIndex: exp.Position.Index,
	}
	if exp.ClusterVersion >= raft.Protocol2 {
		meta.Log.AdoptedFrom = exp.Position.Index
	}
	meta.Membership = coreconsensus.MembershipValue{
		Index: exp.Position.Index, Members: slices.Clone(members), Retired: exp.Membership.Retired,
		RetiredKeys: exp.Membership.RetiredKeys,
	}
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
//
// A member's truncation that would leave no configuration is refused before
// anything is written, the directory still quarantined (#723): with neither
// a snapshot nor a configuration entry before the damage, the node would
// start a learner of nothing, admit no peer, ask no member and stay silent
// for good under a leader that still lists it. WipeAndRejoin is its exit.
//
// So is one that would take the node back to a configuration whose other
// members have all left since, as far as the dropped part of the log still
// names a newer configuration (truncationStrands, #758): a voter that
// acknowledged every replacement of its peers, and that #603's guard
// therefore judged to hold the newest configuration, would restart over
// the old one, ask and admit only removed members and never end its
// silence. Its way out is WipeAndRejoin, then the removal of its old ID on
// the cluster, after one more voter is promoted when MinVoters refuses the
// removal: until then the old ID holds the slot, and the JOIN is refused
// (R8-11).
func (d DataDir) truncateAndStaySilent() error {
	segN, off, err := d.quarantinePoint()
	if err != nil {
		return err
	}
	logDir := filepath.Join(d.d.dir, "log")
	keeps, err := d.truncationKeepsMembers(logDir, segN, off)
	if err != nil {
		return err
	}
	if !keeps {
		return recoverRefused("the truncation leaves no configuration: the node would admit and ask no member; clear the quarantine with WipeAndRejoin")
	}
	strands, err := d.truncationStrands(logDir, segN, off)
	if err != nil {
		return err
	}
	if strands {
		return recoverRefused("the truncation keeps a configuration none of whose other members the log's newest configuration still lists: " +
			"the node would ask and admit only removed members and stay silent for good; clear the quarantine with WipeAndRejoin, " +
			"then Remove this node's ID on the cluster, after a Promote of one more voter when MinVoters refuses the removal, " +
			"so that its slot takes a JOIN again (R8-11)")
	}
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

// truncationKeepsMembers reports whether a member's directory still names
// its members once its log is truncated at (segN, off): a snapshot lists
// them, or a configuration entry lies in what the reopen after the
// truncation keeps. A directory that is not yet a member's needs none: a
// joining one joins again (#632). It changes nothing.
//
// What the reopen keeps is judged as the reopen judges it, batch by batch
// (reopenKeeps): a configuration entry before the damage, in the batch the
// damage tears, is dropped with that batch. Judged record by record, the
// founding entry, which shares its batch with its hard state, and a
// joiner's first entries, which share the batch of the log it replayed,
// passed the guard and were dropped anyway, and the node started silent
// with no configuration (#745).
func (d DataDir) truncationKeepsMembers(logDir string, segN uint64, off int64) (bool, error) {
	if d.d.log.stamp.State != coreconsensus.StampMember {
		return true, nil
	}
	metas, err := d.d.snaps.List()
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(metas, func(m coreconsensus.SnapshotMetaValue) bool { return len(m.Membership.Members) > 0 }) {
		return true, nil
	}
	segs, err := logSegments(logDir)
	if err != nil {
		return false, err
	}
	// The lowest segment first: it holds the founding entry, or the first
	// one a snapshot did not compact.
	for _, n := range slices.Sorted(maps.Keys(segs)) {
		if n > segN || (n == segN && off <= 0) {
			break
		}
		b, err := os.ReadFile(segs[n])
		if err != nil {
			return false, storageFailed("read segment", err)
		}
		end := int64(len(b))
		if n == segN {
			end = off
		}
		if holdsConfiguration(b[:reopenKeeps(b, end)]) {
			return true, nil
		}
	}
	return false, nil
}

// reopenKeeps is how many bytes of segment b the reopen keeps once the
// truncation zeroed it from end: the records it reads in order, through
// the seal or up to the first hole, less the batch that hole tears, which
// never completed and is dropped whole, the records before the damage with
// it (recover, trimTornTail: classifyTail, tornBatchStart). A batch never
// spans two segments, a rotation falls between batches (R8-04), so the
// segments before the damaged one are kept through their seal. It is 0
// when the reopen keeps none: no record, or a tail the reopen would
// quarantine again.
//
// Of the records read it keeps the segment's first, which tornBatchStart
// compares with the torn batch's start, and those of the batch the last
// one belongs to, the only ones classifyTail walks back over: a whole
// segment's records would hold as much again as the segment's bytes.
func reopenKeeps(b []byte, end int64) int64 {
	b = b[:min(int64(len(b)), max(end, 0))]
	var head scanRec
	var batch []scanRec
	var off int64
	for read := 0; off < int64(len(b)); read++ {
		r, ok := parseRecord(b[off:])
		if !ok {
			break
		}
		s := scanRecOf(&r, off)
		switch {
		case read == 0:
			head = s
		case batch[0].batch != s.batch:
			batch = batch[:0]
		}
		batch = append(batch, s)
		off += int64(r.length)
		if r.kind == recSeal {
			return off
		}
	}
	if len(batch) == 0 {
		return 0
	}
	recs := batch
	if batch[0].off != head.off {
		recs = append([]scanRec{head}, batch...)
	}
	trim, _ := classifyTail(b, recs, off)
	return max(trim, 0)
}

// holdsConfiguration reports whether the segment bytes b hold a readable
// configuration entry, scanning every record boundary as maxRecordTerm
// does.
func holdsConfiguration(b []byte) bool {
	for o := 0; o+recHeader+recFixed <= len(b); o += recAlign {
		r, ok := parseRecord(b[o:])
		if !ok {
			continue
		}
		if r.kind == recEntry && r.ekind == uint8(coreconsensus.EntryMembership) {
			return true
		}
		o += r.length - recAlign
	}
	return false
}

// truncationStrands reports whether a member's truncation at (segN, off)
// takes it back to a configuration whose other members have all left since:
// none of them is listed, under the key the kept configuration pins for it,
// in the configuration the whole log leaves in force (truncationConfs). The
// node would restart silent over the kept one, ask only members that no
// longer follow a leader of this cluster for the leader's record (#718),
// refuse at its transport every member it last ran with, and never end its
// silence: the cluster would run with one voter less for good (#758). A
// directory that is not yet a member's needs no configuration, a joining
// one joins again (#632). It changes nothing.
//
// It refuses at best. The part the truncation drops is read as logMaxTerm
// reads it, at every record boundary past the damage, but a configuration
// entry the damage itself took cannot be read: when no configuration entry
// past the kept one can be read there, nothing is known, and the truncation
// is taken. What the reopen keeps is cut as truncationKeepsMembers cuts it
// (reopenKeeps, #745).
func (d DataDir) truncationStrands(logDir string, segN uint64, off int64) (bool, error) {
	if d.d.log.stamp.State != coreconsensus.StampMember {
		return false, nil
	}
	metas, err := d.d.snaps.List()
	if err != nil {
		return false, err
	}
	segs, err := logSegments(logDir)
	if err != nil {
		return false, err
	}
	seen := newTruncationConfs(d.d.log.stamp.ID, metas)
	for _, n := range slices.Sorted(maps.Keys(segs)) {
		b, err := os.ReadFile(segs[n])
		if err != nil {
			return false, storageFailed("read segment", err)
		}
		switch {
		case n < segN:
			seen.read(b[:reopenKeeps(b, int64(len(b)))])
		case n == segN && off > 0:
			keep := reopenKeeps(b, off)
			seen.read(b[:keep])
			seen.cut()
			seen.read(b[keep:])
		default:
			seen.cut()
			seen.read(b)
		}
	}
	return seen.strands(), nil
}

// truncationConfs follows a member's configurations through its log, in
// log order, as the replay and raft apply them: an entry drops every
// configuration entry logged at or past its index, which it overwrote, a
// truncate record those at or past its own (applyEntry, applyTruncate),
// and a configuration entry raft parses takes effect once appended (D8,
// noteAppended). The newest snapshot that names members is in force
// through its index: a configuration logged at or below it is the history
// the snapshot was cut from (raft's base).
//
// cut marks the truncation point. What is read before it is what the reopen
// keeps, and the configuration in force there is the one the node restarts
// with; what is read after it, the dropped part, leaves in force the
// configuration the node last ran with before the damage, as far as it can
// still be read.
type truncationConfs struct {
	snap   *coreconsensus.MembershipValue
	kept   coreconsensus.MembershipValue
	logged []loggedConf
	self   coreconsensus.NodeID
	snapAt uint64
	seq    uint64
	keptAs uint64
	keeps  bool
	isCut  bool
}

// loggedConf is the configuration of the entry at index, seq telling an
// entry from the one that overwrote it at the same index.
type loggedConf struct {
	m     coreconsensus.MembershipValue
	index uint64
	seq   uint64
}

// newTruncationConfs follows the configurations of node self's log over
// metas, its snapshots newest first, as snaps.List gives them.
func newTruncationConfs(self coreconsensus.NodeID, metas []coreconsensus.SnapshotMetaValue) *truncationConfs {
	c := &truncationConfs{self: self}
	if i := slices.IndexFunc(metas, func(m coreconsensus.SnapshotMetaValue) bool { return len(m.Membership.Members) > 0 }); i >= 0 {
		c.snap, c.snapAt = &metas[i].Membership, metas[i].Position.Index
	}
	return c
}

// read applies every readable record of the segment bytes b in order,
// scanning every record boundary as maxRecordTerm does: a hole skips the
// bytes it covers, never the records after it.
func (c *truncationConfs) read(b []byte) {
	for o := 0; o+recHeader+recFixed <= len(b); o += recAlign {
		r, ok := parseRecord(b[o:])
		if !ok {
			continue
		}
		c.apply(&r)
		o += r.length - recAlign
	}
}

// apply applies record r: an entry overwrites what was logged at or past
// its index, and is pushed when it is a configuration raft parses; a
// truncate record drops what was logged at or past its index. No other
// record changes the configurations.
func (c *truncationConfs) apply(r *record) {
	switch r.kind {
	case recEntry:
		c.drop(r.index)
		if r.ekind != uint8(coreconsensus.EntryMembership) {
			return
		}
		a, err := wire.ParseAdminAt(r.ever, r.data)
		if err != nil {
			// raft ignores it at append and keeps the configuration
			// before it in force (noteAppended).
			return
		}
		c.seq++
		c.logged = append(c.logged, loggedConf{m: a.Membership, index: r.index, seq: c.seq})
	case recTruncate:
		c.drop(r.index)
	}
}

// drop forgets the configurations logged at or past index.
func (c *truncationConfs) drop(index uint64) {
	k := len(c.logged)
	for k > 0 && c.logged[k-1].index >= index {
		k--
	}
	c.logged = c.logged[:k]
}

// inForce is the configuration in force at what was read so far, and which
// one it is: the newest logged past the snapshot's index, else the
// snapshot's, as 0; ok is false when there is none.
func (c *truncationConfs) inForce() (m *coreconsensus.MembershipValue, as uint64, ok bool) {
	if n := len(c.logged); n > 0 && c.logged[n-1].index > c.snapAt {
		return &c.logged[n-1].m, c.logged[n-1].seq, true
	}
	return c.snap, 0, c.snap != nil
}

// cut marks the truncation point, once: the configuration in force there
// is the one the node restarts with.
func (c *truncationConfs) cut() {
	if c.isCut {
		return
	}
	c.isCut = true
	if m, as, ok := c.inForce(); ok {
		c.kept, c.keptAs, c.keeps = *m, as, true
	}
}

// strands reports whether the configuration in force past everything read,
// when it is another than the one the node restarts with, strands it there
// (stranded). With no configuration kept, #723's refusal applies instead.
func (c *truncationConfs) strands() bool {
	c.cut()
	newest, as, ok := c.inForce()
	if !c.keeps || !ok || as == c.keptAs {
		return false
	}
	return stranded(c.self, &c.kept, newest)
}

// stranded reports whether newest, which lists a member other than self,
// lists none of kept's other members under the non-zero key kept pins for
// it: every member self would ask and admit over kept is gone from the
// configuration it last ran with. A newest that lists self alone takes no
// peer from it. A key DenyPin withdrew, the zero pin, admits nobody.
func stranded(self coreconsensus.NodeID, kept, newest *coreconsensus.MembershipValue) bool {
	if !slices.ContainsFunc(newest.Members, func(m coreconsensus.MemberValue) bool { return m.ID != self }) {
		return false
	}
	return !slices.ContainsFunc(kept.Members, func(m coreconsensus.MemberValue) bool {
		x, listed := newest.Member(m.ID)
		return m.ID != self && listed && !isZeroDigest(m.SPKI) && x.SPKI == m.SPKI
	})
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
