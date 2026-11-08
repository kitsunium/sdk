package proc_test

import (
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/core/proc"
)

// TestControllerNamesAreTheKernels pins each controller's String to the name
// cgroup.controllers and cgroup.subtree_control spell, and that the unminted
// zero and a value past the last constant are not Known (#727).
func TestControllerNamesAreTheKernels(t *testing.T) {
	t.Parallel()
	want := map[proc.Controller]string{
		proc.ControllerCPU: "cpu", proc.ControllerCPUSet: "cpuset", proc.ControllerIO: "io",
		proc.ControllerMemory: "memory", proc.ControllerPids: "pids", proc.ControllerHugeTLB: "hugetlb",
		proc.ControllerRDMA: "rdma", proc.ControllerMisc: "misc",
	}
	for c, name := range want {
		if !c.Known() || c.String() != name {
			t.Errorf("Controller(%d) = %q, Known %v; want %q", uint8(c), c.String(), c.Known(), name)
		}
	}
	for _, c := range []proc.Controller{0, proc.ControllerMisc + 1, 255} {
		if c.Known() || c.String() != "controller("+strconv.Itoa(int(c))+")" {
			t.Errorf("Controller(%d) = %q, Known %v; want an unknown value", uint8(c), c.String(), c.Known())
		}
	}
}
