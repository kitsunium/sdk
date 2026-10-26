package consensus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// openDirIn opens dir with its boot-scoped marker directory run inside the
// test's own temporary tree, so nothing a wipe removes lies outside it.
func openDirIn(t *testing.T, dir, run string) DataDir {
	t.Helper()
	d, err := NewDataDirWith(dir, DataDirOptions{SegmentBytes: 16 << 10, RuntimeDir: run})
	if err != nil {
		t.Fatalf("open %s: %v %v", dir, err, errs.FieldsOf(err))
	}
	return d
}

// refusedByWipe asserts that err is the STOP_MARKER_PRESENT of STOP-WIPE,
// whose detail names the exit to run again.
func refusedByWipe(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, coreconsensus.StopMarkerPresent) {
		t.Errorf("%s on a directory whose wipe was interrupted: %v, want STOP_MARKER_PRESENT", what, err)
		return
	}
	fields := map[string]string{}
	for _, f := range errs.FieldsOf(err) {
		fields[f.Key()] = f.StringValue()
	}
	if fields["marker"] != markerWipe {
		t.Errorf("%s refused by marker %q, want %s", what, fields["marker"], markerWipe)
	}
}

// WipeAndRejoin deleted the log, then the snapshots, then the stamp and
// the markers, and synced the directory only at the end. Interrupted after
// the log was gone and before the stamp was (a crash, a power cut, a
// failure of the snapshot removal), the directory kept its member
// STAMP with the old NodeID and incarnation, its snapshot, and an empty
// log: no term, no vote, none of the entries past the snapshot. Nothing
// refused that start, and Init, the runbook's next step, handed back the
// old pair: the node rejoined its cluster as the same voter with an
// amnesic log, against ADR 0152 R1-17, and acknowledged writes were lost
// (#690).
//
// The scenario: A leads, B is down, A and X commit and acknowledge e0..e4.
// X is stopped for WipeAndRejoin, which is cut after the log removal. A
// dies. B comes back, X restarts on what the wipe left. The start, Init and
// Recover are now refused STOP-WIPE until the wipe is run again, which
// completes it; Init then draws a new NodeID. The node the exit is made
// for, one stopped STORAGE_FAILED in an earlier boot, whose marker refuses
// nothing in this one, is TestAWipeCutAtEveryStepIsSafe's.
func TestAnInterruptedWipeRefusesTheStartUntilItIsRunAgain(t *testing.T) {
	c, nodes := threeOnDisk(t, 60)
	var a *diskNode
	for a == nil {
		for _, dn := range nodes {
			if dn.node.Status().Role == coreconsensus.RoleLeader {
				a = dn
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	var x, b *diskNode
	for _, dn := range nodes {
		switch {
		case dn == a:
		case x == nil:
			x = dn
		default:
			b = dn
		}
	}
	b.crash(t, c)
	// The memory double keeps a stopped node's queue across its restart: B
	// is cut off, so no frame A sends while B is down reaches it later, as
	// none would over a real network.
	c.net.Isolate(b.id, true)

	// e0..e4: committed and acknowledged by A and X alone.
	var tok coreconsensus.TokenValue
	for i := range 5 {
		tok = proposeOnDisk(t, []*diskNode{a, x}, fmt.Sprintf("e%d=acked%d", i, i))
	}
	c.waitAppliedDisk([]*diskNode{a, x}, tok.Index)
	c.net.Isolate(a.id, true) // nothing A sends from here on reaches anyone
	x.crash(t, c)

	run := t.TempDir()
	x.dd = openDirIn(t, x.dir, run)
	before := must(Inspect(x.dd))(t)
	if len(before.Snapshots) == 0 || before.Snapshots[0].Position.Index >= tok.Index {
		t.Skipf("X's newest snapshot does not lie below e4 (%d): the scenario needs one", tok.Index)
	}
	// The interruption: the wipe stops right after it deleted the log, as
	// a crash there would, or a failure to empty the snapshot directory.
	errCut := errors.New("cut after the log")
	x.dd.d.afterWipeStep = func(step string) error {
		if step == "log" {
			return errCut
		}
		return nil
	}
	if err := ClearQuarantine(x.dd, WipeAndRejoin, "op:wipe"); !errors.Is(err, errCut) {
		t.Fatalf("the cut wipe answered %v, want the cut", err)
	}
	if err := x.dd.Close(); err != nil {
		t.Logf("close after the interrupted wipe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(x.dir, fileStamp)); err != nil {
		t.Fatalf("the stamp is gone (%v): the injection no longer cuts the wipe between the log and the stamp", err)
	}

	a.crash(t, c) // the leader that held e0..e4 with X dies
	c.net.Isolate(b.id, false)
	b.open(t, c)
	x.dd = openDirIn(t, x.dir, run)
	after := must(Inspect(x.dd))(t)
	if !slices.Contains(after.Markers, markerWipe) {
		t.Errorf("the interrupted wipe left the stop markers %v, want %s among them", after.Markers, markerWipe)
	}
	// The runbook's next step, Init under a NEW NodeID, must not hand back
	// the old pair of a half-wiped directory.
	if id, _, err := Init(x.dd, c.cid, after.Stamp.Slot); err == nil {
		t.Errorf("Init after the interrupted wipe answered id %d (old id %d)", id, x.id)
	} else {
		refusedByWipe(t, "Init", err)
	}
	x.sm = newKV()
	n, err := NewNode(diskConfig(c, x))
	if err == nil {
		x.node = n
		lost := amnesicVoterLoses(t, c, b, x)
		t.Fatalf("X restarted on the half-wiped directory under its old stamp (id %d) as a voter of %v; acknowledged writes lost: %v",
			x.id, x.node.Membership().Voters(), lost)
	}
	refusedByWipe(t, "NewNode", err)

	// Run again, the wipe completes, and Init draws a new pair.
	if err := ClearQuarantine(x.dd, WipeAndRejoin, "op:wipe"); err != nil {
		t.Fatalf("WipeAndRejoin run again: %v", err)
	}
	r := must(Inspect(x.dd))(t)
	if r.Stamp.State != 0 || len(r.Markers) != 0 || len(r.Snapshots) != 0 || r.Last >= r.First {
		t.Fatalf("the completed wipe left stamp state %d, markers %v, %d snapshots, log %d..%d", r.Stamp.State, r.Markers, len(r.Snapshots), r.First, r.Last)
	}
	id, inc, err := Init(x.dd, c.cid, after.Stamp.Slot)
	if err != nil {
		t.Fatalf("Init after the completed wipe: %v", err)
	}
	if id == x.id || inc == before.Stamp.Incarnation {
		t.Fatalf("Init after the wipe reused the old identity: id %d (old %d)", id, x.id)
	}
	if err := x.dd.Close(); err != nil {
		t.Fatal(err)
	}
}

// amnesicVoterLoses runs what followed the start the fix refuses: B and X
// elect a leader, which commits an entry of its own term, and it reports
// every one of e0..e4 either node lost.
func amnesicVoterLoses(t *testing.T, c *cluster, b, x *diskNode) []string {
	t.Helper()
	after1 := proposeOnDisk(t, []*diskNode{b, x}, "after=1")
	c.waitAppliedDisk([]*diskNode{b, x}, after1.Index)
	var lost []string
	for _, dn := range []*diskNode{b, x} {
		for i := range 5 {
			if _, ok := dn.sm.get(fmt.Sprintf("e%d", i)); !ok {
				lost = append(lost, fmt.Sprintf("node %d lacks e%d", dn.id, i))
			}
		}
	}
	return lost
}

// copyDir copies the data directory src, inside the test's temporary
// tree, to dst, a fresh path inside it too: the disk a crash at that point
// leaves. The lock file is not copied, its content is nothing and Windows
// refuses to read a locked range; the open creates it again.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case e.IsDir():
			return os.MkdirAll(to, 0o700)
		case rel == fileLock || !e.Type().IsRegular():
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// removeEach removes, inside dir, the entries of sub whose position in the
// sorted listing keep does not accept: a removal a crash cut midway.
func removeEach(t *testing.T, dir, sub string, keep func(i int, name string) bool) {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, sub))
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range ents {
		if !keep(i, e.Name()) {
			if err := os.Remove(filepath.Join(dir, sub, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestAWipeCutAtEveryStepIsSafe cuts WipeAndRejoin after each of its steps
// and inside each removal: the directory is copied at that point, in the
// test's temporary tree, and every copy is opened as a restart would. While
// the wipe is not done, the copy refuses a start, Init, Redraw, Recover,
// RestoreSnapshot and TruncateAndStaySilent on STOP-WIPE, and the wipe run
// again completes, after which Init draws a new NodeID and incarnation. A
// crash before STOP-WIPE is durable leaves the member whole, one after its
// removal an empty directory (#690).
func TestAWipeCutAtEveryStepIsSafe(t *testing.T) {
	c, nodes := threeOnDisk(t, 60)
	// Entries of 4 KiB past the snapshot: the log spans several segments,
	// so a removal cut midway leaves a hole.
	big := strings.Repeat("x", 4<<10)
	var tok coreconsensus.TokenValue
	for i := range 12 {
		tok = proposeOnDisk(t, nodes, fmt.Sprintf("big%d=%s", i, big))
	}
	c.waitAppliedDisk(nodes, tok.Index)
	for _, dn := range nodes {
		dn.crash(t, c)
	}
	x := nodes[0]
	if segs := must(os.ReadDir(filepath.Join(x.dir, "log")))(t); len(segs) < 3 {
		t.Fatalf("the scenario needs a log of several segments: %d entries in log/", len(segs))
	}
	run, root := t.TempDir(), t.TempDir()
	x.dd = openDirIn(t, x.dir, run)
	defer func() {
		if err := x.dd.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	before := must(Inspect(x.dd))(t)
	if len(before.Snapshots) == 0 || before.Last <= before.Snapshots[0].Position.Index {
		t.Fatalf("the scenario needs a snapshot and entries past it: %d snapshots, log %d..%d", len(before.Snapshots), before.First, before.Last)
	}
	// Markers the wipe removes besides the stamp: a STORAGE_FAILED stop of
	// an earlier boot and a silence condition.
	if err := os.WriteFile(filepath.Join(x.dir, markerStopPrefix+"STORAGE_FAILED"), []byte("boot=an-earlier-boot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileSync(filepath.Join(x.dir, fileSilent), silenceRecord(before.LastTerm, 0)); err != nil {
		t.Fatal(err)
	}
	cut := map[string]string{"before": filepath.Join(root, "before")}
	copyDir(t, x.dir, cut["before"])
	var steps []string
	x.dd.d.afterWipeStep = func(step string) error {
		steps = append(steps, step)
		cut[step] = filepath.Join(root, step)
		copyDir(t, x.dir, cut[step])
		return nil
	}
	if err := ClearQuarantine(x.dd, WipeAndRejoin, "op:wipe"); err != nil {
		t.Fatalf("WipeAndRejoin: %v", err)
	}
	if want := []string{"marker", "log", "snap", "stamp", "done"}; !slices.Equal(steps, want) {
		t.Fatalf("the wipe's steps %v, want %v", steps, want)
	}
	// Inside a removal: half of the log's segments gone, the first among
	// them; one snapshot's data and another's metadata gone; the stamp gone
	// before the other markers.
	partial := func(name, from, sub string, keep func(int, string) bool) {
		cut[name] = filepath.Join(root, name)
		copyDir(t, cut[from], cut[name])
		removeEach(t, cut[name], sub, keep)
	}
	partial("log-midway", "marker", "log", func(i int, _ string) bool { return i%2 == 1 })
	seenSnap, seenMeta := false, false
	partial("snap-midway", "log", "snap", func(_ int, n string) bool {
		switch {
		case !seenSnap && filepath.Ext(n) == ".snap":
			seenSnap = true
			return false
		case seenSnap && !seenMeta && filepath.Ext(n) == ".meta":
			seenMeta = true
			return false
		}
		return true
	})
	partial("stamp-midway", "snap", ".", func(_ int, n string) bool { return n != fileStamp })

	next := coreconsensus.ClusterID{29}
	for _, name := range []string{"before", "marker", "log-midway", "log", "snap-midway", "snap", "stamp-midway", "stamp", "done"} {
		t.Run(name, func(t *testing.T) {
			dd := openDirIn(t, cut[name], filepath.Join(root, "run-"+name))
			defer func() {
				if err := dd.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			}()
			r := must(Inspect(dd))(t)
			switch name {
			case "before":
				// The wipe had not begun: the member is whole.
				if slices.Contains(r.Markers, markerWipe) || r.Stamp != before.Stamp || r.Last != before.Last {
					t.Fatalf("a wipe cut before STOP-WIPE left markers %v, stamp state %d, log ..%d (was ..%d)", r.Markers, r.Stamp.State, r.Last, before.Last)
				}
				return
			case "done":
			default:
				if !slices.Contains(r.Markers, markerWipe) {
					t.Fatalf("a wipe cut at %s left no %s: markers %v", name, markerWipe, r.Markers)
				}
				dn := &diskNode{dir: cut[name], dd: dd, id: x.id, sm: newKV()}
				if n, err := NewNode(diskConfig(&cluster{t: t, net: NewMemoryNetwork(), cid: c.cid}, dn)); err == nil {
					_ = n.HardClose()
					t.Fatalf("a wipe cut at %s let the node start under its old stamp", name)
				} else {
					refusedByWipe(t, "NewNode", err)
				}
				_, _, err := Init(dd, c.cid, before.Stamp.Slot)
				refusedByWipe(t, "Init", err)
				_, err = Redraw(dd)
				refusedByWipe(t, "Redraw", err)
				refusedByWipe(t, "Recover", Recover(dd, next, nil, &RecoverOptions{Confirm: next.String(), Operator: "op:recover"}))
				_, err = RestoreSnapshot(dd, nil, next, nil, &RestoreOptions{Confirm: next.String(), Operator: "op:restore"})
				refusedByWipe(t, "RestoreSnapshot", err)
				refusedByWipe(t, "TruncateAndStaySilent", ClearQuarantine(dd, TruncateAndStaySilent, "op:truncate"))
				if err := ClearQuarantine(dd, WipeAndRejoin, "op:wipe"); err != nil {
					t.Fatalf("WipeAndRejoin run again after a cut at %s: %v", name, err)
				}
				r = must(Inspect(dd))(t)
			}
			if r.Stamp.State != 0 || len(r.Markers) != 0 || len(r.Snapshots) != 0 || r.Last >= r.First {
				t.Fatalf("after %s the directory keeps stamp state %d, markers %v, %d snapshots, log %d..%d", name, r.Stamp.State, r.Markers, len(r.Snapshots), r.First, r.Last)
			}
			if _, err := os.Stat(filepath.Join(cut[name], fileSilent)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("after %s the silence condition is still there: %v", name, err)
			}
			id, inc, err := Init(dd, c.cid, before.Stamp.Slot)
			if err != nil {
				t.Fatalf("Init after %s: %v", name, err)
			}
			if id == x.id || inc == before.Stamp.Incarnation {
				t.Fatalf("Init after %s reused the old identity: id %d (old %d)", name, id, x.id)
			}
		})
	}
}
