//go:build unix

// Package rlimit_test — what a child inherits as its open-file soft limit,
// before and after KeepRaisedNoFile.
package rlimit_test

import (
	"bufio"
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// keepRaisedHelperEnv makes the test binary run as the helper process of
// TestKeepRaisedNoFile instead of as a test suite.
const keepRaisedHelperEnv string = "SDK_RLIMIT_KEEPRAISED_HELPER"

// helperSoft is the soft RLIMIT_NOFILE the helper is started under: below any
// hard limit a CI host sets, so the Go runtime's raise at the helper's start is
// visible as a difference.
const helperSoft uint64 = 256

// TestKeepRaisedNoFile starts a helper — this test binary again — under a soft
// RLIMIT_NOFILE of helperSoft. The Go runtime raises the helper's own soft limit
// to the hard limit at start, and puts helperSoft back in every child it forks
// until the program sets RLIMIT_NOFILE itself (#1091). The helper spawns
// `ulimit -Sn` directly — os/exec, no trampoline — before and after
// KeepRaisedNoFile: before, the child prints helperSoft, lower than the
// helper's own; after, it prints the helper's own raised limit, the value
// KeepRaisedNoFile returned, and a second call returns the same.
func TestKeepRaisedNoFile(t *testing.T) {
	//: the helper half: report, call, report, and leave.
	if os.Getenv(keepRaisedHelperEnv) != "" {
		runKeepRaisedHelper(t)
		return
	}
	t.Parallel()
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatalf("Getrlimit(RLIMIT_NOFILE) = %v", err)
	}
	//: the raise is only visible when the hard limit is above the helper's soft.
	if uint64(lim.Max) <= helperSoft {
		t.Skipf("hard RLIMIT_NOFILE %d leaves no room above %d", uint64(lim.Max), helperSoft)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	//: the shell lowers its soft limit, then becomes the helper in place.
	cmd := osexec.Command("/bin/sh", "-c",
		"ulimit -S -n "+strconv.FormatUint(helperSoft, 10)+` && exec "$0" -test.run='^TestKeepRaisedNoFile$' -test.count=1`, self)
	cmd.Env = append(os.Environ(), keepRaisedHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper = %v\n%s", err, out)
	}
	got := helperReport(t, string(out))
	if got["before"] != helperSoft {
		t.Errorf("a direct spawn before KeepRaisedNoFile inherited %d, want the pre-raise %d", got["before"], helperSoft)
	}
	if got["self"] <= got["before"] {
		t.Errorf("the helper's own soft limit %d is not above its children's %d: the runtime did not raise it", got["self"], got["before"])
	}
	if got["after"] != got["self"] || got["kept"] != got["self"] || got["again"] != got["self"] {
		t.Errorf("after KeepRaisedNoFile: report %v, want after = kept = again = self", got)
	}
}

// runKeepRaisedHelper is the helper process's body: it prints what a direct
// child inherits, its own soft limit, KeepRaisedNoFile's answer, what a child
// inherits after it, and a second call's answer, one key=value per line.
func runKeepRaisedHelper(t *testing.T) {
	t.Helper()
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		t.Fatalf("Getrlimit = %v", err)
	}
	before := childSoftNoFile(t)
	kept, err := rlimit.KeepRaisedNoFile()
	if err != nil {
		t.Fatalf("KeepRaisedNoFile = %v", err)
	}
	after := childSoftNoFile(t)
	again, err := rlimit.KeepRaisedNoFile()
	if err != nil {
		t.Fatalf("KeepRaisedNoFile again = %v", err)
	}
	if kept.Hard != uint64(lim.Max) {
		t.Fatalf("KeepRaisedNoFile Hard = %d, want %d", kept.Hard, uint64(lim.Max))
	}
	report := "before=" + strconv.FormatUint(before, 10) + "\n" +
		"self=" + strconv.FormatUint(uint64(lim.Cur), 10) + "\n" +
		"kept=" + strconv.FormatUint(kept.Soft, 10) + "\n" +
		"after=" + strconv.FormatUint(after, 10) + "\n" +
		"again=" + strconv.FormatUint(again.Soft, 10) + "\n"
	if _, werr := os.Stdout.WriteString(report); werr != nil {
		t.Fatalf("writing the report = %v", werr)
	}
}

// childSoftNoFile spawns `ulimit -Sn` directly and returns what it printed.
func childSoftNoFile(t *testing.T) uint64 {
	t.Helper()
	out, err := osexec.Command("/bin/sh", "-c", "ulimit -S -n").Output()
	if err != nil {
		t.Fatalf("ulimit -Sn = %v", err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatalf("ulimit -Sn printed %q", out)
	}
	return n
}

// helperReport parses the helper's key=value lines.
func helperReport(t *testing.T, out string) map[string]uint64 {
	t.Helper()
	got := map[string]uint64{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			continue
		}
		got[key] = n
	}
	if len(got) != 5 {
		t.Fatalf("helper report = %v from\n%s", got, out)
	}
	return got
}
