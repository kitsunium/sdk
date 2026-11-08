//go:build linux

// White-box tests of what S0g (#734) asks of a Linux group: Members over the
// subtree, as Contains and populated reach it; DeleteBeneath, a Delegate=yes
// unit's cleanup that keeps the unit's group; a watch that publishes a change
// a burst undid; Membership, a group's directory as /proc/<pid>/cgroup names
// it. Each runs on a fake cgroup filesystem under t.TempDir().
package cgroup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/internal/cgroupfs"
)

// TestMembersReachTheSubtree: a process a delegated service moved into a
// group of its own is still a member — Members lists what Contains finds and
// populated counts, the group's own cgroup.procs and every one beneath it.
// The group itself gone is CGROUP_READ_FAILED; a group beneath that went is
// skipped.
func TestMembersReachTheSubtree(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	writeFile(t, filepath.Join(g.dir, procsFile), "101\n")
	for _, sub := range []string{"service", "service/worker"} {
		if err := os.Mkdir(filepath.Join(g.dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(g.dir, "service", procsFile), "202\n")
	writeFile(t, filepath.Join(g.dir, "service", "worker", procsFile), "303\n304\n")
	members, err := g.Members()
	slices.Sort(members)
	if err != nil || !slices.Equal(members, []int{101, 202, 303, 304}) {
		t.Fatalf("Members = %v, %v; want the whole subtree [101 202 303 304]", members, err)
	}
	if in, cerr := g.Contains(303); cerr != nil || !in {
		t.Fatalf("Contains(303) = %v, %v: Members and Contains disagree", in, cerr)
	}
	if _, err := missingGroup(t).Members(); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("Members of a missing group = %v, want CGROUP_READ_FAILED", err)
	}
}

// TestDeleteBeneathKeepsTheGroup: the groups a delegated service made beneath
// the unit's group go, deepest first, and the group stays, its files kept; a
// second call finds nothing to do. A group beneath still holding something
// stops it with CGROUP_DELETE_FAILED naming that group, and a link beneath is
// never followed: its target, a witness outside, keeps its content.
func TestDeleteBeneathKeepsTheGroup(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	writeFile(t, filepath.Join(g.dir, procsFile), "")
	for _, dir := range []string{"a/b/c", "a/d", "e"} {
		if err := os.MkdirAll(filepath.Join(g.dir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.DeleteBeneath(); err != nil {
		t.Fatalf("DeleteBeneath = %v", err)
	}
	entries, err := os.ReadDir(g.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != procsFile {
		t.Fatalf("the group holds %v (%v), want its own file alone", entries, err)
	}
	if err := g.DeleteBeneath(); err != nil {
		t.Fatalf("DeleteBeneath with nothing beneath = %v", err)
	}
	busy := fakeGroup(t)
	if err := os.Mkdir(filepath.Join(busy.dir, "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(busy.dir, "held", procsFile), "404\n")
	if err := busy.DeleteBeneath(); !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) || fieldValue(err, "dir") != filepath.Join(busy.dir, "held") {
		t.Fatalf("DeleteBeneath over a held group = %v, want CGROUP_DELETE_FAILED naming it", err)
	}
	linked := fakeGroup(t)
	witness := t.TempDir()
	writeFile(t, filepath.Join(witness, "kept"), "kept")
	if err := os.Symlink(witness, filepath.Join(linked.dir, "out")); err != nil {
		t.Fatal(err)
	}
	if err := linked.DeleteBeneath(); err != nil {
		t.Fatalf("DeleteBeneath beside a link = %v: a link is no group", err)
	}
	if got := readFile(t, filepath.Join(witness, "kept")); got != "kept" {
		t.Fatalf("the link's target lost its content: %q", got)
	}
	if err := missingGroup(t).DeleteBeneath(); !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) {
		t.Fatalf("DeleteBeneath of a missing group = %v, want CGROUP_DELETE_FAILED", err)
	}
}

// TestWatchPublishesAChangeABurstUndid: the kernel announced a change of
// cgroup.events, and the watch read the file only once the burst was over —
// populated 0, 1, 0 — so the state it read is the one it published last.
// It publishes it again: a reader that read the group meanwhile (proc/service
// reads Members when it decides) learns that the group emptied. A rewrite of
// the same state stands for that burst.
func TestWatchPublishesAChangeABurstUndid(t *testing.T) {
	g := fakeGroup(t)
	events := filepath.Join(g.dir, eventsFile)
	writeFile(t, events, "populated 0\nfrozen 0\n")
	ch, err := g.Watch(t.Context())
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	if first := <-ch; first.Populated {
		t.Fatalf("first value %+v, want empty", first)
	}
	writeFile(t, events, "populated 0\nfrozen 0\n")
	if v, open := nextValue(t, ch, func(coreproc.GroupEventsValue) bool { return true }); !open || v.Populated {
		t.Fatalf("after an announced change that changed nothing: %+v (open %v), want the state again", v, open)
	}
}

// TestMembership: a group's directory as /proc/<pid>/cgroup names it — the
// path below the cgroup2 mount that holds it, under the part of the
// hierarchy that mount exposes; the deepest mount wins; a path no mount holds,
// or one relative or unclean, is INVALID_SPEC.
func TestMembership(t *testing.T) {
	t.Parallel()
	const (
		whole = "36 35 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n"
		bind  = "1775 1733 0:29 /user.slice /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n"
		inner = "36 35 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n37 36 0:31 /system.slice /run/sys rw - cgroup2 cgroup2 rw\n"
	)
	for _, c := range []struct {
		mountinfo, dir, want string
	}{
		{whole, "/sys/fs/cgroup/system.slice/web.service", "/system.slice/web.service"},
		{whole, "/sys/fs/cgroup", "/"},
		{bind, "/sys/fs/cgroup/app.scope/unit", "/user.slice/app.scope/unit"},
		{inner, "/run/sys/web.service", "/system.slice/web.service"},
		{whole, "/sys/fs/cgroupx/web", ""},
		{whole, "sys/fs/cgroup/web", ""},
		{whole, "/sys/fs/cgroup/a/../web", ""},
	} {
		readFile := func(name string) ([]byte, error) {
			if name == cgroupfs.SelfMountinfoFile {
				return []byte(c.mountinfo), nil
			}
			return nil, os.ErrNotExist
		}
		got, err := membershipIn(readFile, c.dir)
		if c.want == "" {
			if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
				t.Errorf("membership(%q) = %q, %v; want INVALID_SPEC", c.dir, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("membership(%q) = %q, %v; want %q", c.dir, got, err, c.want)
		}
	}
}

// TestMembershipIsOwnGroupsInverse: on this host, the directory OwnGroup
// derives from /proc/self/cgroup turns back into the "0::" path itself — the
// path an AttachValue bounds an adoption with, which a group's Path is not.
// It reads and writes nothing of the hierarchy; it skips without cgroup v2.
func TestMembershipIsOwnGroupsInverse(t *testing.T) {
	t.Parallel()
	dir, ok := cgroupfs.OwnGroup(os.ReadFile)
	if !ok {
		t.Skip("no unified cgroup membership on this host")
	}
	raw, err := os.ReadFile(cgroupfs.SelfCgroupFile)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := cgroupfs.Membership(string(raw))
	got, err := Membership(dir)
	if err != nil || got != want {
		t.Fatalf("Membership(%q) = %q, %v; want /proc/self/cgroup's %q", dir, got, err, want)
	}
}
