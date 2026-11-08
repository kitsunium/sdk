//go:build linux

package cgroup_test

import (
	"os"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestStatOnADelegatedGroup is #932's live acceptance on a delegated cgroup v2
// host (the linux-init lanes): a child of this test spinning in a group it
// created reports CPU time and a pid. Elsewhere it skips: nothing is written
// to a hierarchy this test did not create.
func TestStatOnADelegatedGroup(t *testing.T) {
	//: only a delegated hierarchy takes a group of the test's own.
	if !cgroup.Available() {
		t.Skip("cgroup v2 is not delegated to this test")
	}
	g, err := cgroup.Create("sdk-stat-" + time.Now().Format("150405.000000000"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = g.Kill(); time.Sleep(100 * time.Millisecond); _ = g.Delete() })
	child, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "end=$(($(date +%s)+2)); while [ $(date +%s) -lt $end ]; do :; done"},
		&os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	if err != nil {
		t.Skipf("no /bin/sh: %v", err)
	}
	defer func() { _ = child.Kill(); _, _ = child.Wait() }()
	if err := g.Add(child.Pid); err != nil {
		t.Fatalf("Add: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	reader, ok := g.(coreproc.GroupReader)
	if !ok {
		t.Fatal("the Linux group is no GroupReader")
	}
	stat, err := reader.Stat()
	if err != nil || stat.CPUUsage < 150*time.Millisecond || stat.PidsCurrent < 1 {
		t.Fatalf("Stat = %+v, %v; want >= 150 ms of CPU and a pid", stat, err)
	}
}
