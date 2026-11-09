package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestJobObjectStat pins #932's Windows row on an empty job — this test never
// confines its own process: the accounting reads, Partial set, and the
// process list is empty.
func TestJobObjectStat(t *testing.T) {
	g, err := cgroup.Create("probe")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer g.Delete()
	reader, ok := g.(coreproc.GroupReader)
	if !ok {
		t.Fatal("the Job Object group is no GroupReader")
	}
	stat, err := reader.Stat()
	if err != nil || !stat.Partial {
		t.Fatalf("Stat of an empty job = %+v, %v; want a partial reading", stat, err)
	}
	members, err := reader.Members()
	if err != nil || len(members) != 0 {
		t.Fatalf("Members of an empty job = %v, %v", members, err)
	}
}
