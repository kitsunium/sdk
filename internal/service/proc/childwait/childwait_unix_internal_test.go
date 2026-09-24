//go:build unix

// Package childwait — the real wait4(-1): children that exit with known codes,
// collected by ReapAny, each status landing on the claim of the spawn that
// forked it. This is the only file in the package that creates processes, and
// wait4(-1) collects every child of the test binary, so its tests do not run
// in parallel with one another. The fork here is the test's own; production
// forks only in service/proc/exec, which is what the reaper's hand-off suite
// drives end to end.
package childwait

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// shellPath is the program every child runs. POSIX requires it at this path.
const shellPath string = "/bin/sh"

// reapDeadline bounds the drain, so a sweep that never collects a child fails
// the test instead of hanging it.
const reapDeadline time.Duration = 30 * time.Second

// requireShell skips the test where no executable /bin/sh exists: without one
// no child can be spawned, and the host can say nothing about the ledger.
func requireShell(t *testing.T) {
	t.Helper()
	info, err := os.Stat(shellPath)
	//: a missing or non-executable shell is the host's gap, not the ledger's.
	if err != nil || info.Mode()&0o111 == 0 {
		t.Skipf("%s is not an executable file here (%v): no child can be spawned", shellPath, err)
	}
}

// startShell forks /bin/sh exiting with code, returning its process.
func startShell(code int) (*os.Process, error) {
	//: an inherited, empty environment is all `exit N` needs.
	return os.StartProcess(shellPath, []string{"sh", "-c", "exit " + strconv.Itoa(code)},
		&os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
}

// TestReapAnyHandsEachStatusToItsClaim spawns claimed children with distinct
// exit codes plus one nobody claims, drains them all with ReapAny, and requires
// every claim to hold exactly its own child's status and the unclaimed one to
// leave nothing behind.
func TestReapAnyHandsEachStatusToItsClaim(t *testing.T) {
	type tc struct {
		name  string
		codes []int
	}
	tests := []tc{
		{"four claimed children and one orphan", []int{0, 1, 7, 42}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		requireShell(t)
		claims := make(map[int]*Claim, len(c.codes))
		want := make(map[int]int, len(c.codes))
		//: claim one child per exit code.
		for _, code := range c.codes {
			proc, claim, err := Spawn(func() (*os.Process, error) { return startShell(code) })
			//: a failed spawn leaves nothing to collect.
			if err != nil {
				t.Fatalf("Spawn(exit %d): %v", code, err)
			}
			claims[proc.Pid] = claim
			want[proc.Pid] = code
		}
		orphan, err := startShell(3)
		//: the unclaimed child must exist for its absence of a claim to mean anything.
		if err != nil {
			t.Fatalf("starting the unclaimed child: %v", err)
		}
		collected := drainAll(t, len(c.codes)+1)
		//: the sweep collects the unclaimed child like any other.
		if !collected[orphan.Pid] {
			t.Errorf("%s: the unclaimed child (pid %d) was never collected", c.name, orphan.Pid)
		}
		//: every claimed child's status reached its own claim, unaltered.
		for pid, claim := range claims {
			status, ok := claim.Collected()
			//: a claim left empty is the defect.
			if !ok {
				t.Errorf("%s: pid %d: no status on its claim", c.name, pid)
				continue
			}
			//: the status is exactly that child's normal exit.
			if !status.WaitStatus.Exited() || status.WaitStatus.ExitStatus() != want[pid] {
				t.Errorf("%s: pid %d: status %#x, want a normal exit %d", c.name, pid, uint32(status.WaitStatus), want[pid])
			}
		}
		//: collected claims are retired and the orphan left nothing behind.
		if n := pending(book); n != 0 {
			t.Errorf("%s: %d claims left in the ledger, want 0", c.name, n)
		}
	}
	//: run every case in turn: wait4(-1) is process-wide.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestReapAnyWithNoChildren pins wait4's own answer when there is nothing to
// collect: ECHILD, and no pid — the reaper reads it as a clean end of a sweep.
func TestReapAnyWithNoChildren(t *testing.T) {
	type tc struct {
		name string
	}
	tests := []tc{{"a process with no children"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pid, err := ReapAny()
		//: no child at all is ECHILD, never a collected pid.
		if !errors.Is(err, syscall.ECHILD) || pid > 0 {
			t.Errorf("%s: ReapAny() = (%d, %v), want (_, ECHILD)", c.name, pid, err)
		}
	}
	//: run every case in turn: wait4(-1) is process-wide.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// drainAll calls ReapAny until n children have been collected, returning the
// set of collected pids. A child that has not exited yet makes a sweep return
// 0, so the loop simply sweeps again, as the reaper does on its next SIGCHLD.
func drainAll(t *testing.T, n int) map[int]bool {
	t.Helper()
	collected := make(map[int]bool, n)
	deadline := time.Now().Add(reapDeadline)
	//: sweep until every child is in, or out of patience.
	for len(collected) < n && time.Now().Before(deadline) {
		pid, err := ReapAny()
		//: EINTR is a retry, and nothing ready yet is a retry after a pause.
		if errors.Is(err, syscall.EINTR) || (err == nil && pid == 0) {
			time.Sleep(time.Millisecond)
			continue
		}
		//: any other error ends the drain: the children are ours, ECHILD is not expected.
		if err != nil {
			t.Fatalf("ReapAny: %v after %d of %d children", err, len(collected), n)
		}
		collected[pid] = true
	}
	//: a child never collected means a sweep lost it.
	if len(collected) < n {
		t.Fatalf("collected %d of %d children before the deadline", len(collected), n)
	}
	return collected
}
