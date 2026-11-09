//go:build linux

// Package cgroup_test — the live rows of BENCH.md (#1097): Create, Add, the
// Set*Max setters, Kill and Delete against a real cgroupfs, which the
// internal benchmarks of cgroup_linux_bench_test.go could not time on a host
// that delegates nothing.
//
// They need a cgroup v2 root delegated to the benchmarking user — the same one
// the conformance suite finds (delegatedRoot): KITSUNIUM_CGROUP_TEST_ROOT, the
// mount for root, or this process's own group under a `systemd-run --user
// --scope -p Delegate=yes`. Without one every row skips with its reason, so no
// number is ever a refusal's in disguise (BenchmarkCreate_Unavailable is that
// number, and the one this host used to have). The setters need the root's
// children to get their controller (childrenGet): under a scope that holds the
// benchmark itself they do not, and skip; a scope that first moves into a
// leaf and enables memory and pids (see CLAUDE.md) runs them.
//
// A group is made under the root with a name of the benchmark's own and every
// one is removed before the benchmark returns. Only the sleeping children a row
// starts are moved or killed.
package cgroup_test

import (
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// liveBatch is how many groups a row makes before the timer stops to remove
// them: a benchmark is run for as many iterations as it takes to fill its
// time, and a group costs the kernel memory while it exists.
const liveBatch int = 512

// liveSeq numbers the groups of one process so no two benchmarks share a name.
var liveSeq atomic.Int64

// liveSink keeps an error the compiler cannot prove unused.
var liveSink error

// liveBenchRoot returns the delegated root and the options Create takes under it, or skips.
func liveBenchRoot(b *testing.B) (root string, opts []cgroup.Option) {
	b.Helper()
	root, why := delegatedRoot()
	if root == "" {
		b.Skip(why)
	}
	return root, []cgroup.Option{cgroup.WithRoot(root)}
}

// liveGroup makes a group under the live root, removed when the benchmark ends.
func liveGroup(b *testing.B, opts []cgroup.Option) coreproc.Group {
	b.Helper()
	g, err := cgroup.Create("sdk-bench-"+strconv.Itoa(int(liveSeq.Add(1))), opts...)
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	b.Cleanup(func() { _ = g.Delete() })
	return g
}

// liveBatchOf makes n groups, untimed, for a row that times what happens to them.
func liveBatchOf(b *testing.B, opts []cgroup.Option, n int) []coreproc.Group {
	b.Helper()
	groups := make([]coreproc.Group, 0, n)
	for range n {
		g, err := cgroup.Create("sdk-bench-"+strconv.Itoa(int(liveSeq.Add(1))), opts...)
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		groups = append(groups, g)
	}
	return groups
}

// removeAll deletes groups, untimed.
func removeAll(b *testing.B, groups []coreproc.Group) {
	b.Helper()
	for _, g := range groups {
		if err := g.Delete(); err != nil {
			b.Fatalf("Delete: %v", err)
		}
	}
}

// liveChild starts a sleeping child of the benchmark and reaps it at the end.
func liveChild(b *testing.B) *exec.Cmd {
	b.Helper()
	child := exec.Command("sleep", "600")
	if err := child.Start(); err != nil {
		b.Skipf("no sleep(1): %v", err)
	}
	b.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child
}

// BenchmarkCreate_Live is Create alone: one mkdir(2) in cgroupfs, plus the
// name check, the path join and the handle. The groups are removed in batches
// of liveBatch with the timer stopped.
func BenchmarkCreate_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	made := make([]coreproc.Group, 0, liveBatch)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		g, err := cgroup.Create("sdk-bench-"+strconv.Itoa(int(liveSeq.Add(1))), opts...)
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		made = append(made, g)
		if len(made) == liveBatch {
			b.StopTimer()
			removeAll(b, made)
			made = made[:0]
			b.StartTimer()
		}
	}
	b.StopTimer()
	removeAll(b, made)
}

// BenchmarkDelete_Live is Delete alone: one rmdir(2) of an empty group, the
// groups made in batches of liveBatch with the timer stopped.
func BenchmarkDelete_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	var batch []coreproc.Group
	next := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if next == len(batch) {
			b.StopTimer()
			batch, next = liveBatchOf(b, opts, liveBatch), 0
			b.StartTimer()
		}
		liveSink = batch[next].Delete()
		next++
	}
	b.StopTimer()
	removeAll(b, batch[next:])
}

// BenchmarkCreate_Delete_Live is a group's whole life: Create then Delete, the
// pair a caller that makes a group per job pays.
func BenchmarkCreate_Delete_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		g, err := cgroup.Create("sdk-bench-"+strconv.Itoa(int(liveSeq.Add(1))), opts...)
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		liveSink = g.Delete()
	}
}

// BenchmarkAdd_Live is Add: one write of a pid to cgroup.procs, which migrates
// the task. A sleeping child of the benchmark moves between two groups, so each
// write is a real migration and never a pid already in place.
func BenchmarkAdd_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	groups := [2]coreproc.Group{liveGroup(b, opts), liveGroup(b, opts)}
	child := liveChild(b)
	pid := child.Process.Pid
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		liveSink = groups[i&1].Add(pid)
		i++
	}
	b.StopTimer()
	_ = child.Process.Kill()
	_ = child.Wait()
}

// liveSetter runs a Set*Max row: the values alternate so each write changes the
// file, and the row skips where the root's children get no such controller.
func liveSetter(b *testing.B, controller string, set func(g coreproc.Group, v int64) error, values [2]int64) {
	b.Helper()
	root, opts := liveBenchRoot(b)
	enabled, err := childrenGet(root, controller)
	if err != nil {
		b.Fatalf("reading what %q enables for its children: %v", root, err)
	}
	if !enabled {
		b.Skipf("%q does not enable %s for its children: the scope holds the benchmark (see the file comment)", root, controller)
	}
	g := liveGroup(b, opts)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		liveSink = set(g, values[i&1])
		i++
	}
}

// BenchmarkSetMemoryMax_Live is SetMemoryMax: one write of memory.max, which
// the memory controller parses and applies to the live group.
func BenchmarkSetMemoryMax_Live(b *testing.B) {
	liveSetter(b, "memory", func(g coreproc.Group, v int64) error { return g.SetMemoryMax(v) }, [2]int64{64 << 20, 128 << 20})
}

// BenchmarkSetPidsMax_Live is SetPidsMax: one write of pids.max.
func BenchmarkSetPidsMax_Live(b *testing.B) {
	liveSetter(b, "pids", func(g coreproc.Group, v int64) error { return g.SetPidsMax(v) }, [2]int64{64, 128})
}

// BenchmarkKill_Empty_Live is Kill of a group with no member: the write of
// cgroup.kill, which walks the (empty) subtree.
func BenchmarkKill_Empty_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	g := liveGroup(b, opts)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		liveSink = g.Kill()
	}
}

// BenchmarkKill_Populated_Live is Kill of a group holding one sleeping child:
// the write that sends the SIGKILL. The child is started, moved in and — once
// the kill has returned — reaped with the timer stopped, so only Kill is timed.
// Run it with a fixed -benchtime (…x): every iteration spawns a process.
func BenchmarkKill_Populated_Live(b *testing.B) {
	_, opts := liveBenchRoot(b)
	g := liveGroup(b, opts)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		child := exec.Command("sleep", "600")
		if err := child.Start(); err != nil {
			b.Skipf("no sleep(1): %v", err)
		}
		if err := g.Add(child.Process.Pid); err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			b.Fatalf("Add: %v", err)
		}
		b.StartTimer()
		liveSink = g.Kill()
		b.StopTimer()
		_ = child.Wait()
		b.StartTimer()
	}
}
