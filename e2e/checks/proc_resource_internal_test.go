// Package checks — the cgroup and reaper conformance checks.
package checks

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
)

// Test_containsPID pins the membership test the placement check's whole verdict
// rests on.
//
// cgroup.procs is a newline-separated list of pids with a trailing newline, so a
// naive substring search would report pid 42 as present in a group holding only
// 4242 — and the placement check would then pass for a child that ran entirely
// outside its group, which is the exact bug it exists to catch.
func Test_containsPID(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// procs is the cgroup.procs body the kernel reported.
		procs string
		// want is the pid being looked for.
		want string
		// wantFound is whether it is a member.
		wantFound bool
	}
	tests := []tc{
		{name: "the only member", procs: "42\n", want: "42", wantFound: true},
		{name: "one of several members", procs: "7\n42\n1337\n", want: "42", wantFound: true},
		{name: "the last member with no trailing newline", procs: "7\n42", want: "42", wantFound: true},
		{name: "an empty group", procs: "", want: "42"},
		{name: "a group holding someone else", procs: "7\n1337\n", want: "42"},
		{
			//: a substring search would call this a match, and the placement
			//: check would pass for a child that ran outside its group.
			name:  "a longer pid that merely contains the wanted digits",
			procs: "4242\n", want: "42",
		},
		{
			name:  "a shorter pid the wanted one contains",
			procs: "4\n", want: "42",
		},
		{name: "a member surrounded by blank lines", procs: "\n42\n\n", want: "42", wantFound: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := containsPID(c.procs, c.want); got != c.wantFound {
			t.Fatalf("containsPID(%q, %q) = %v, want %v", c.procs, c.want, got, c.wantFound)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cgroupEnvironmental pins the line between "this host does not delegate
// cgroups" and "the SDK broke".
//
// CI commonly runs without delegation, so a matcher that was too NARROW would
// turn every such run red for a reason nobody can fix in the code. One that was
// too WIDE would hide a real confinement regression as an environmental skip,
// which is worse: the row still reads as "nothing to see".
func Test_cgroupEnvironmental(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// err is what the SDK returned.
		err error
		// want is whether it is a host gap rather than a defect.
		want bool
	}
	tests := []tc{
		{name: "no delegated hierarchy", err: perrs.Wrap(coreproc.CgroupUnavailable, perrs.WrapParams{}), want: true},
		{name: "a refused create", err: perrs.Wrap(coreproc.CgroupCreateFailed, perrs.WrapParams{}), want: true},
		//: a write failure on a group we just created IS a defect.
		{name: "a refused write", err: perrs.Wrap(coreproc.CgroupWriteFailed, perrs.WrapParams{})},
		{name: "the off-platform contract", err: perrs.Wrap(coreproc.UnsupportedPlatform, perrs.WrapParams{})},
		{name: "an untyped error", err: errors.New("boom")},
		{name: "no error at all"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := cgroupEnvironmental(c.err); got != c.want {
			t.Fatalf("cgroupEnvironmental(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_readCgroupFile pins that the TRAILING NEWLINE is stripped.
//
// Every cgroup v2 interface file ends in one, and every comparison in the
// read-back check is against a bare value — so without the trim each of them
// would fail on a kernel that took the limit correctly, and report a
// confinement bug that is not there.
func Test_readCgroupFile(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// content is what the kernel would expose in the file.
		content string
		// want is the value the check must compare against.
		want string
		// missing omits the file entirely.
		missing bool
	}
	tests := []tc{
		{name: "a value with the kernel's trailing newline", content: "max\n", want: "max"},
		{name: "a numeric limit", content: strconv.Itoa(1<<20) + "\n", want: strconv.Itoa(1 << 20)},
		{name: "a value with no newline", content: "max", want: "max"},
		{name: "an empty file", content: "", want: ""},
		{name: "a multi-line file", content: "a\nb\n", want: "a\nb"},
		{name: "a file that does not exist", missing: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		const file string = "memory.max"
		if !c.missing {
			if err := os.WriteFile(filepath.Join(dir, file), []byte(c.content), 0o600); err != nil {
				t.Fatalf("staging the interface file: %v", err)
			}
		}

		got, err := readCgroupFile(dir, file)

		if c.missing {
			//: an unreadable file means we cannot confirm the kernel's view, and
			//: the caller has to be told rather than handed an empty string.
			if err == nil {
				t.Fatalf("readCgroupFile on a missing file = %q, want an error", got)
			}
			return
		}
		if err != nil {
			t.Fatalf("readCgroupFile = %v, want nil", err)
		}
		if got != c.want {
			t.Fatalf("readCgroupFile = %q, want %q — the comparison is against a "+
				"bare value, so a surviving newline fails a correct kernel", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// The cgroup and reaper checks reach the kernel and need delegation the host may
// not grant, so what is pinned here is the property that holds EVERYWHERE: the
// check runs to completion and produces a row the runner can tally. Whether it
// passes, degrades off-platform or skips for want of delegation is the
// conformance lane's question — a laptop without a delegated v2 hierarchy must
// not turn any of them red.

// Test_cgroupConformance pins the limit round-trip row: set a limit, read it
// back from /sys/fs/cgroup, and prove the kernel took it rather than the SDK
// merely recording it.
func Test_cgroupConformance(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(cgroupConformance(), cgroupDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cgroupPreExecPlacement pins the placement row — that a child is a member
// of its group from its FIRST instruction, with no unconfined window between
// exec and a post-spawn Add.
func Test_cgroupPreExecPlacement(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(cgroupPreExecPlacement(), cgroupDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_reaperSubreaper pins the subreaper row.
//
// It is deliberately NOT parallel: arming a child-subreaper changes state for
// the whole process, so a sibling test spawning a child while this one runs
// would see its own orphans reparent here.
func Test_reaperSubreaper(t *testing.T) {
	//: serial — SetChildSubreaper mutates process-global state.
	if problem := wellFormedRow(reaperSubreaper(), reaperDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_reaperIsPID1 pins the pid-1 probe row, which is what tells a container
// init apart from an ordinary process.
func Test_reaperIsPID1(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(reaperIsPID1(), reaperDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_reaperConstruct pins the construction row: a reaper can be built at all
// on this platform, before anything asks it to adopt.
func Test_reaperConstruct(t *testing.T) {
	t.Parallel()
	if problem := wellFormedRow(reaperConstruct(), reaperDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_reaperOrphanAdoption pins the adoption row — an orphaned grandchild
// reparents to us and is reaped, which is the only way to observe a subreaper
// working from inside the process that armed it.
//
// Serial for the same reason as Test_reaperSubreaper: it arms the subreaper and
// then counts what comes home, so a sibling test's orphan would be
// indistinguishable from its own.
func Test_reaperOrphanAdoption(t *testing.T) {
	//: serial — it arms the subreaper and counts adopted children.
	if problem := wellFormedRow(reaperOrphanAdoption(), reaperDomain); problem != nil {
		t.Fatal(problem)
	}
}
