package consensus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The fault-point sweep of ADR 0152 D15's shim lane (#1113). Each scenario
// runs once over a faultFS that only counts, which gives its N operations
// — the open of its data directory included —, then once per operation
// index k in 1..N twice: operation k failed (EIO, ENOSPC for a write) with
// the rest let through and a power cut at the end dropping whatever was not
// synced, and the process stopped at k with a power cut that keeps, drops
// or tears (k mod 3, torn pages by k) what was not synced. The directory is
// then reopened, the boot's STORAGE_FAILED markers cleared as the reboot
// and the operator would, and judged by the oracles the package already
// holds: the open is not quarantined and serves its log, no entry a Sync
// acknowledged before the cut is lost or changed, a snapshot a Commit
// acknowledged is listed with its bytes, one a Remove acknowledged is not,
// and an offline operation the cut interrupted completes when run again,
// to the state a run without faults leaves, Inspect reading it.
//
// Every k of a scenario is one run of a few milliseconds: the scenarios
// are sized so that the sweep stays well inside the 60 s a Bazel shard of
// consensus_test has under race and coverage, each scenario a top-level
// test of its own (N = 73, 27, 28, 29, 37 and 47 on 2026-11-12; 0.3 to 14 s
// each under -race on a loaded darwin/arm64 host).
//
// Negative control: writeBatch reordered to write and sync the end record
// before the body, the #417 defect, fails the append scenario first at
// operation 18 of 73 (the body's write, failed or stopped with the cut
// keeping what was written), the reopen refusing LOG_QUARANTINED, "damage
// inside a batch whose end record is valid"; the change was reverted.

// faultOutcome is what a scenario saw acknowledged before the cut.
type faultOutcome struct {
	// acked is the last log index a Sync that returned nil made durable,
	// commit the commit of the hard state it carried.
	acked, commit uint64
	// compacted is the compaction point a Sync that returned nil made
	// durable, 0 for none.
	compacted uint64
	// published and removed say a Commit, a Remove, returned nil.
	published, removed bool
	// done says the scenario's operation returned nil.
	done bool
}

// faultScenario is one write ordering the sweep runs at every operation.
type faultScenario struct {
	name string
	// seg is the segment size of the scenario's data directory.
	seg int64
	// template builds, once, the directory every run starts from.
	template func(t *testing.T, dir string)
	// run is the scenario over d, opened under the shim; it reports what
	// it saw acknowledged. It does not run when the open failed.
	run func(d DataDir, o *faultOutcome)
	// check judges d, reopened after the cut, against o; then it returns
	// the state it ends in, which every run must share with the run
	// without faults, "" when nothing is compared.
	check func(d DataDir, o *faultOutcome) (string, error)
	// quarantined says the scenario starts from a log its open
	// quarantines: a reopen may find it so until the exit completes.
	quarantined bool
	// mustSee names operations of the run without faults the scenario is
	// about: one of each must be in its trace ("kind name-prefix").
	mustSee []string
}

// The six scenarios of #1113, each a top-level test so that Bazel deals
// them to six shards of consensus_test: `go test -run TestEveryFaultPoint`
// runs them all. Each runs its scenario at every operation index, under a
// failure and under a stop of the process, judges every directory the cut
// leaves with the package's oracles, and logs its N.

func TestEveryFaultPointOfAnAppendAcrossARotation(t *testing.T) {
	sweepFaultPoints(t, appendAcrossARotation())
}

func TestEveryFaultPointOfACompactionDroppingASegment(t *testing.T) {
	sweepFaultPoints(t, compactionDroppingASegment())
}

func TestEveryFaultPointOfACutPublishedThenRetained(t *testing.T) {
	sweepFaultPoints(t, cutPublishedThenRetained())
}

func TestEveryFaultPointOfAnInstallSpooledThenCommitted(t *testing.T) {
	sweepFaultPoints(t, installSpooledThenCommitted())
}

func TestEveryFaultPointOfARecover(t *testing.T) {
	sweepFaultPoints(t, newOfflineTemplates(t).recoverScenario())
}

func TestEveryFaultPointOfTruncateAndStaySilent(t *testing.T) {
	sweepFaultPoints(t, newOfflineTemplates(t).truncateScenario())
}

// sweepFaultPoints runs sc without faults, then at every operation index.
func sweepFaultPoints(t *testing.T, sc faultScenario) {
	template := t.TempDir()
	sc.template(t, template)
	clean, s := runFaultScenario(t, sc, template, faultNone, 0)
	n := s.count()
	if n == 0 {
		t.Fatal("setup: the scenario made no operation")
	}
	for _, want := range sc.mustSee {
		if !slicesContainsPrefix(s.trace, want) {
			t.Fatalf("setup: no %q among the %d operations of the run without faults: %v", want, n, s.trace)
		}
	}
	t.Logf("%s: N = %d operations", sc.name, n)
	for k := 1; k <= n; k++ {
		for _, kind := range []faultKind{faultFail, faultCrash} {
			got, fs := runFaultScenario(t, sc, template, kind, k)
			if got.err != nil {
				t.Errorf("%s, %s at operation %d of %d (%s): %v", sc.name, faultName(kind, k), k, n, fs.opAt(k), got.err)
				continue
			}
			if got.state != clean.state {
				t.Errorf("%s, %s at operation %d of %d (%s): ends in %s, the run without faults in %s",
					sc.name, faultName(kind, k), k, n, fs.opAt(k), got.state, clean.state)
			}
		}
	}
}

// slicesContainsPrefix reports whether one of trace's "n kind name" lines
// is want's "kind name-prefix".
func slicesContainsPrefix(trace []string, want string) bool {
	for _, line := range trace {
		if _, rest, ok := strings.Cut(line, " "); ok && strings.HasPrefix(rest, want) {
			return true
		}
	}
	return false
}

// faultName names a run: the failure, or the stop and its cut.
func faultName(kind faultKind, k int) string {
	if kind == faultFail {
		return "a failure"
	}
	return "a stop, the cut " + cutAt(k).String()
}

// cutAt is the cut of the stop at operation k: keep, drop and tear in
// turn.
func cutAt(k int) cutKind { return cutKind(k % 3) }

// faultRun is the verdict of one run: the oracle's error, or the state the
// directory ends in.
type faultRun struct {
	err   error
	state string
}

// runFaultScenario runs sc from a copy of template under a shim told kind
// at operation k, cuts the power, reopens, and judges.
func runFaultScenario(t *testing.T, sc faultScenario, template string, kind faultKind, k int) (faultRun, *faultFS) {
	t.Helper()
	dir := t.TempDir()
	if err := copyTree(template, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { forgetBoot(t, dir) })
	s := newFaultFS()
	s.inline = true
	s.kind, s.at = kind, k
	opts := DataDirOptions{SegmentBytes: sc.seg, RuntimeDir: testRuntimeDir()}
	o := &faultOutcome{}
	if d, err := newDataDirOn(dir, opts, s.disk()); err == nil {
		sc.run(d, o)
		abandon(d.Close())
	}
	cut := cutDrop
	if kind == faultCrash {
		cut = cutAt(k)
	}
	if err := s.settle(cut, uint64(k)); err != nil {
		t.Fatal(err)
	}
	if err := restartProcess(dir); err != nil {
		t.Fatal(err)
	}
	d, err := newDataDirOn(dir, opts, newPassThrough())
	if err != nil {
		return faultRun{err: fmt.Errorf("the reopen after the cut: %w %v", err, errs.FieldsOf(err))}, s
	}
	defer func() { abandon(d.Close()) }()
	if l := d.d.log; l.refuse != nil && !(sc.quarantined && errs.HasCode(l.refuse, coreconsensus.CodeLogQuarantined)) {
		return faultRun{err: fmt.Errorf("the reopened log refuses every call: %w %v", l.refuse, errs.FieldsOf(l.refuse))}, s
	}
	state, err := sc.check(d, o)
	return faultRun{err: err, state: state}, s
}

// newPassThrough is a seam that counts and syncs nothing: the reopen and
// the oracles after a cut, which judge files no power cut follows.
func newPassThrough() *diskFS {
	s := newFaultFS()
	s.inline = true
	return s.disk()
}

// restartProcess is what a restart of the stopped process and the reboot
// the power cut was leave of dir: no in-process memory of an unmarked stop,
// and no STORAGE_FAILED marker of this boot — the reboot clears the
// boot-scoped one, the operator the one in the directory once the device is
// replaced. A marker D4 required is no oracle's concern here:
// TestASyncThatFailsAtOpenRefusesTheSameBootRestart pins it.
func restartProcess(dir string) error {
	if abs, err := filepath.Abs(dir); err == nil {
		unmarked.Delete(abs)
	}
	unmarked.Delete(dir)
	l := &fileLog{dir: dir, runDir: testRuntimeDir()}
	for _, p := range []string{l.runtimeMarker("STORAGE_FAILED"), filepath.Join(dir, markerStopPrefix+"STORAGE_FAILED")} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// faultHS is the hard state a scenario's batch carries.
func faultHS(commit uint64) *coreconsensus.HardState {
	return &coreconsensus.HardState{Term: 1, Commit: commit}
}

// syncedLog builds a directory whose log holds entries 1..last of term 1,
// synced in batches of batch.
func syncedLog(t *testing.T, dir string, seg int64, last, batch uint64) {
	t.Helper()
	d := openDir(t, dir, seg)
	for lo := uint64(1); lo <= last; lo += batch {
		hi := min(lo+batch-1, last)
		if err := d.Log().Append(ents(lo, hi, 1), faultHS(lo-1)); err != nil {
			t.Fatal(err)
		}
		mustSync(t, d.Log())
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// appendBatches appends batches of 8 entries after from until to, each
// synced, recording what each Sync acknowledged.
func appendBatches(l coreconsensus.LogStore, from, to uint64, o *faultOutcome) bool {
	for lo := from; lo <= to; lo += 8 {
		hs := faultHS(lo - 1)
		if l.Append(ents(lo, lo+7, 1), hs) != nil || l.Sync() != nil {
			return false
		}
		o.acked, o.commit = lo+7, hs.Commit
	}
	return true
}

// checkLog judges a reopened log: it serves first..last, the entries
// ents wrote, the acknowledged ones all there, the compaction no further
// than a Sync acknowledged it past what it kept, and the hard state at
// least the one acknowledged.
func checkLog(l coreconsensus.LogStore, o *faultOutcome, keptFrom uint64) error {
	first, last := l.Bounds()
	if last < o.acked {
		return fmt.Errorf("the log ends at %d: the entries through %d a Sync acknowledged are lost", last, o.acked)
	}
	if first > keptFrom {
		return fmt.Errorf("the log starts at %d: entry %d, never compacted, is lost", first, keptFrom)
	}
	if last >= first {
		got, err := l.Entries(first, last+1, 1<<30, nil)
		if err != nil {
			return fmt.Errorf("reading %d..%d: %w", first, last, err)
		}
		for _, e := range got {
			want := fmt.Sprintf("entry-%d-%d", e.Position.Index, 1)
			if string(e.Data) != want || e.Position.Term != 1 {
				return fmt.Errorf("entry %d reads %q term %d, want %q", e.Position.Index, e.Data, e.Position.Term, want)
			}
		}
		if uint64(len(got)) != last-first+1 {
			return fmt.Errorf("%d entries read of %d..%d", len(got), first, last)
		}
	}
	hs, err := l.HardState()
	if err != nil {
		return err
	}
	if hs.Commit < o.commit {
		return fmt.Errorf("the hard state's commit is %d: the %d a Sync acknowledged is lost", hs.Commit, o.commit)
	}
	return nil
}

// appendAcrossARotation appends batches across two rotations of 4 KiB
// segments: writeBatch, the seal, createSegment, publishNext.
func appendAcrossARotation() faultScenario {
	return faultScenario{
		name: "an append batch across a rotation", seg: 4096,
		template: func(t *testing.T, dir string) { syncedLog(t, dir, 4096, 3, 3) },
		run: func(d DataDir, o *faultOutcome) {
			o.acked = 3
			o.done = appendBatches(d.Log(), 4, 4+8*11, o)
		},
		check: func(d DataDir, o *faultOutcome) (string, error) {
			return "", checkLog(d.Log(), o, 1)
		},
		mustSee: []string{"rename seg-", "syncdir log"},
	}
}

// compactionDroppingASegment compacts through two points and syncs a batch
// after each: the compaction record, then the segments it drops and the
// log directory's sync.
func compactionDroppingASegment() faultScenario {
	return faultScenario{
		name: "a compaction dropping a segment", seg: 4096,
		template: func(t *testing.T, dir string) { syncedLog(t, dir, 4096, 72, 8) },
		run: func(d DataDir, o *faultOutcome) {
			o.acked = 72
			l := d.Log()
			for i, through := range []uint64{48, 64} {
				if l.CompactPrefix(through) != nil {
					return
				}
				from := 73 + 8*uint64(i)
				if !appendBatches(l, from, from, o) {
					return
				}
				o.compacted = through
			}
			o.done = true
		},
		check: func(d DataDir, o *faultOutcome) (string, error) {
			return "", checkLog(d.Log(), o, 65)
		},
		mustSee: []string{"remove seg-"},
	}
}

// snapBody is the encoded stream of the scenario's snapshot at index: a
// chunk per ten entries, as confStream writes them.
func snapBody(index uint64) []byte { return confStream(int(index / 10)) }

// snapMeta is that snapshot's metadata.
func snapMeta(index uint64) coreconsensus.SnapshotMetaValue {
	return confMeta(snapshotID(index, 1), index, int64(len(snapBody(index))))
}

// spoolSnapshot spools the snapshot at index into s in parts writes and
// commits it, reporting whether every call returned nil.
func spoolSnapshot(s coreconsensus.SnapshotStore, index uint64, parts int) bool {
	m := snapMeta(index)
	sink, _, err := s.Create(m.ID, m)
	if err != nil {
		return false
	}
	body := snapBody(index)
	step := (len(body) + parts - 1) / parts
	for off := 0; off < len(body); off += step {
		if _, err := sink.Write(body[off:min(off+step, len(body))]); err != nil {
			abandon(sink.Abort())
			return false
		}
	}
	return sink.Commit(m) == nil
}

// checkSnapshots judges a reopened store: every snapshot it lists opens
// with the bytes its index wrote, those of always are listed, the one a
// Commit acknowledged is listed and the one a Remove acknowledged is not.
func checkSnapshots(s coreconsensus.SnapshotStore, o *faultOutcome, always []uint64, published, removed uint64) error {
	metas, err := s.List()
	if err != nil {
		return fmt.Errorf("listing the snapshots: %w", err)
	}
	listed := map[uint64]bool{}
	for _, m := range metas {
		listed[m.Position.Index] = true
		h, err := s.Open(m.ID)
		if err != nil {
			return fmt.Errorf("opening listed snapshot %s: %w", m.ID, err)
		}
		b, err := io.ReadAll(io.NewSectionReader(h, 0, m.EncodedSize+1))
		abandon(h.Close())
		if err != nil || !bytes.Equal(b, snapBody(m.Position.Index)) || m.EncodedSize != int64(len(b)) {
			return fmt.Errorf("listed snapshot %s holds %d bytes (%v), not the %d its Commit wrote", m.ID, len(b), err, len(snapBody(m.Position.Index)))
		}
	}
	for _, i := range always {
		if !listed[i] {
			return fmt.Errorf("the snapshot at %d, which the scenario never touched, is not listed", i)
		}
	}
	if o.published && !listed[published] {
		return fmt.Errorf("the snapshot at %d a Commit acknowledged is not listed", published)
	}
	if o.removed && listed[removed] {
		return fmt.Errorf("the snapshot at %d a Remove acknowledged is still listed", removed)
	}
	return nil
}

// cutPublishedThenRetained publishes a cut, then removes the oldest of the
// three as retention keeps the two newest (D7): the spool's sync, its
// rename, the .meta written atomically last, the removal and its
// directory's sync.
func cutPublishedThenRetained() faultScenario {
	return faultScenario{
		name: "a cut published then retained", seg: 4096,
		template: func(t *testing.T, dir string) {
			d := openDir(t, dir, 4096)
			for _, i := range []uint64{10, 20} {
				if !spoolSnapshot(d.Snapshots(), i, 1) {
					t.Fatalf("setup: the snapshot at %d was not published", i)
				}
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		},
		run: func(d DataDir, o *faultOutcome) {
			if o.published = spoolSnapshot(d.Snapshots(), 30, 1); !o.published {
				return
			}
			o.removed = d.Snapshots().Remove(snapshotID(10, 1)) == nil
			o.done = o.removed
		},
		check: func(d DataDir, o *faultOutcome) (string, error) {
			return "", checkSnapshots(d.Snapshots(), o, []uint64{20}, 30, 10)
		},
		mustSee: []string{"rename " + snapshotID(30, 1) + ".spool", "rename " + snapshotID(30, 1) + ".meta.tmp", "remove " + snapshotID(10, 1) + ".meta"},
	}
}

// installSpooledThenCommitted receives a snapshot past the log's end in
// several writes, commits it, then moves the log onto it as an install
// does: compacted past its end with the snapshot's term, synced.
func installSpooledThenCommitted() faultScenario {
	return faultScenario{
		name: "an install spooled then committed", seg: 4096,
		template: func(t *testing.T, dir string) { syncedLog(t, dir, 4096, 24, 8) },
		run: func(d DataDir, o *faultOutcome) {
			o.acked = 24
			if o.published = spoolSnapshot(d.Snapshots(), 40, 3); !o.published {
				return
			}
			if d.d.log.compactPrefixAt(40, 1) != nil || d.Log().Sync() != nil {
				return
			}
			o.compacted, o.acked, o.done = 40, 0, true
		},
		check: func(d DataDir, o *faultOutcome) (string, error) {
			if err := checkSnapshots(d.Snapshots(), o, nil, 40, 0); err != nil {
				return "", err
			}
			first, last := d.Log().Bounds()
			switch {
			case o.compacted == 0 && first == 1:
				return "", checkLog(d.Log(), o, 1)
			case first == 41 && last == 40:
				if term, err := d.Log().Term(40); err != nil || term != 1 {
					return "", fmt.Errorf("the log moved onto the snapshot reads the term %d (%v) at its index, want 1", term, err)
				}
				return "", nil
			}
			return "", fmt.Errorf("the log is %d..%d: neither the one before the install nor the one after it", first, last)
		},
		mustSee: []string{"rename " + snapshotID(40, 1) + ".spool"},
	}
}

// offlineTemplates is the directories of a three-node cluster on disk,
// crashed, the offline scenarios start from: the survivor Recover keeps,
// and a victim whose synced data was damaged for TruncateAndStaySilent.
type offlineTemplates struct {
	survivor, victim string
	next             coreconsensus.ClusterID
	members          []coreconsensus.MemberValue
	opt              *RecoverOptions
	// ask stands for the live members TruncateAndStaySilent reads the
	// configuration from (#763): they answer the one the cluster ran with.
	ask *QuarantineOptions
}

// newOfflineTemplates runs the cluster once and copies its directories.
func newOfflineTemplates(t *testing.T) *offlineTemplates {
	c, nodes := threeOnDisk(t, 30)
	conf := nodes[0].node.Membership()
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	o := &offlineTemplates{survivor: t.TempDir(), victim: t.TempDir(), next: coreconsensus.ClusterID{13}, ask: askingAnswer(conf)}
	keep := nodes[0]
	keep.dd = openDir(t, keep.dir, 16<<10)
	o.members, o.opt = recoverArgs(t, c, nodes, keep, o.next)
	if err := keep.dd.Close(); err != nil {
		t.Fatal(err)
	}
	for dst, src := range map[string]string{o.survivor: keep.dir, o.victim: nodes[1].dir} {
		if err := copyTree(src, dst); err != nil {
			t.Fatal(err)
		}
	}
	damageSyncedRecord(t, o.victim)
	return o
}

// recoverScenario runs Recover on the survivor: the STOP-RECOVER marker,
// the forced entry, the stamp, the audit line, the marker's removal. One
// the cut interrupted completes when run again.
func (o *offlineTemplates) recoverScenario() faultScenario {
	return faultScenario{
		name: "a Recover", seg: 16 << 10,
		template: func(t *testing.T, dir string) {
			if err := copyTree(o.survivor, dir); err != nil {
				t.Fatal(err)
			}
		},
		run: func(d DataDir, out *faultOutcome) {
			out.done = Recover(d, o.next, o.members, o.opt) == nil
		},
		check: func(d DataDir, out *faultOutcome) (string, error) {
			_, err := os.Stat(filepath.Join(d.Dir(), markerRecover))
			st, serr := d.Log().Stamp()
			if serr != nil {
				return "", fmt.Errorf("the stamp: %w", serr)
			}
			if err == nil || st.Log.Cluster != o.next {
				if out.done {
					return "", fmt.Errorf("a Recover that returned nil left the stamp of cluster %s, its marker present %v", st.Log.Cluster, err == nil)
				}
				if err := Recover(d, o.next, o.members, o.opt); err != nil {
					return "", fmt.Errorf("the Recover run again: %w %v", err, errs.FieldsOf(err))
				}
			}
			// A Recover resumed from its marker writes its forced entry
			// again over the one the cut left, a term above it: the term
			// of the last entry is the one thing a resumed run may end
			// with that a whole one does not, and nothing of the new
			// history was acknowledged under the lower one.
			return offlineState(d, false)
		},
		mustSee: []string{"rename " + markerRecover, "remove " + markerRecover, "rename " + fileStamp},
	}
}

// truncateScenario opens the damaged victim, which quarantines it, and
// takes the TruncateAndStaySilent exit: SILENT, the truncation, the
// marker's removal, the reopen and SILENT's index. One the cut interrupted
// completes when taken again.
func (o *offlineTemplates) truncateScenario() faultScenario {
	return faultScenario{
		name: "TruncateAndStaySilent", seg: 16 << 10, quarantined: true,
		template: func(t *testing.T, dir string) {
			if err := copyTree(o.victim, dir); err != nil {
				t.Fatal(err)
			}
		},
		run: func(d DataDir, out *faultOutcome) {
			out.done = ClearQuarantineWith(context.Background(), d, TruncateAndStaySilent, "op:silent", o.ask) == nil
		},
		check: func(d DataDir, out *faultOutcome) (string, error) {
			if errs.HasCode(d.d.log.refuse, coreconsensus.CodeLogQuarantined) {
				if out.done {
					return "", errors.New("an exit that returned nil left the log quarantined")
				}
				if err := ClearQuarantineWith(context.Background(), d, TruncateAndStaySilent, "op:silent", o.ask); err != nil {
					return "", fmt.Errorf("TruncateAndStaySilent taken again: %w %v", err, errs.FieldsOf(err))
				}
			}
			term, _, ok := silenceRecordIn(d.Dir())
			if !ok {
				return "", errors.New("no silence condition once the exit completed")
			}
			state, err := offlineState(d, true)
			return fmt.Sprintf("%s, silent from term %d", state, term), err
		},
		mustSee: []string{"rename " + fileSilent, "remove " + markerQuarantine},
	}
}

// offlineState is what Inspect reads of d, which must read it, and the
// log's bounds — with the last entry's term when withTerm is set: the state
// every run of an offline scenario must end in.
func offlineState(d DataDir, withTerm bool) (string, error) {
	r, err := Inspect(d)
	if err != nil {
		return "", fmt.Errorf("Inspect: %w %v", err, errs.FieldsOf(err))
	}
	first, last := d.Log().Bounds()
	state := fmt.Sprintf("cluster %s kept %d, last %d, log %d..%d", r.Stamp.Log.Cluster, r.Stamp.Log.KeptIndex, r.Last, first, last)
	if withTerm {
		state += fmt.Sprintf(", last term %d", r.LastTerm)
	}
	return state, nil
}
