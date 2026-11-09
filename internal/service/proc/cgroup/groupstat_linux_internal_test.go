//go:build linux

package cgroup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestStatReadsTheGroupFiles pins #932's Linux reader on a group directory
// written under the test's own temporary directory: every counter read from
// its file, a missing optional file zero with Partial, and a missing cpu.stat
// — a deleted group — CGROUP_READ_FAILED.
func TestStatReadsTheGroupFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		cpuStatFile:       "usage_usec 250000\nuser_usec 200000\nsystem_usec 50000\nnr_periods 9\nnr_throttled 3\nthrottled_usec 1500\n",
		memoryCurrentFile: "1048576\n",
		memoryEventsFile:  "low 1\nhigh 2\nmax 3\noom 4\noom_kill 5\n",
		pidsCurrentFile:   "7\n",
		ioStatFile:        "8:0 rbytes=10 wbytes=20 rios=1 wios=2 dbytes=0 dios=0\n253:1 rbytes=5 wbytes=6 rios=7 wios=8\n",
		procsFile:         "101\n202\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g := &controlGroup{dir: dir}
	stat, err := g.Stat()
	if err != nil {
		t.Fatalf("Stat = %v", err)
	}
	want := coreproc.GroupStatValue{
		CPUUsage: 250 * time.Millisecond, CPUUser: 200 * time.Millisecond, CPUSystem: 50 * time.Millisecond,
		ThrottledTime: 1500 * time.Microsecond, NrThrottled: 3, MemoryCurrent: 1 << 20, PidsCurrent: 7,
		MemoryEvents: coreproc.MemoryEventsValue{Low: 1, High: 2, Max: 3, OOM: 4, OOMKill: 5},
		Partial:      true, // no memory.peak: a kernel before 5.19
	}
	io := stat.IO
	stat.IO = nil
	//: every counter from its file, and Partial for the missing peak.
	if !reflect.DeepEqual(stat, want) {
		t.Fatalf("Stat = %+v, want %+v", stat, want)
	}
	if len(io) != 2 || io[0] != (coreproc.IOStatValue{Major: 8, RBytes: 10, WBytes: 20, RIOs: 1, WIOs: 2}) ||
		io[1] != (coreproc.IOStatValue{Major: 253, Minor: 1, RBytes: 5, WBytes: 6, RIOs: 7, WIOs: 8}) {
		t.Fatalf("IO = %+v", io)
	}
	members, err := g.Members()
	if err != nil || len(members) != 2 || members[0] != 101 || members[1] != 202 {
		t.Fatalf("Members = %v, %v; want [101 202]", members, err)
	}
	//: with memory.peak the reading is whole.
	if err := os.WriteFile(filepath.Join(dir, memoryPeakFile), []byte("2097152\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stat, err = g.Stat(); err != nil || stat.Partial || stat.MemoryPeak != 2<<20 {
		t.Fatalf("Stat with memory.peak = %+v, %v", stat, err)
	}
	//: a deleted group.
	gone := &controlGroup{dir: filepath.Join(dir, "gone")}
	if _, err := gone.Stat(); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("Stat of a deleted group = %v, want CGROUP_READ_FAILED", err)
	}
	if _, err := gone.Members(); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("Members of a deleted group = %v, want CGROUP_READ_FAILED", err)
	}
}
