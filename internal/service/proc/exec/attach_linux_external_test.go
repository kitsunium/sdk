//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package exec_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cgroupBound is the cgroup bound TestAttachPIDFile adopts within on Linux:
// this process's own cgroup v2 path, which the child it spawned shares;
// skipped on a host without one.
func cgroupBound(t *testing.T) (cgroup string, code errs.Code, skip string) {
	t.Helper()
	cg, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", 0, "no /proc/self/cgroup"
	}
	for line := range strings.SplitSeq(string(cg), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, 0, ""
		}
	}
	return "", 0, "no cgroup v2 path"
}
