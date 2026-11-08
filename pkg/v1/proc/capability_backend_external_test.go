package proc_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	perrs "github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/proc/cgroup"
	"github.com/kitsunium/sdk/pkg/v1/proc/process"
	"github.com/kitsunium/sdk/pkg/v1/proc/reaper"
	"github.com/kitsunium/sdk/pkg/v1/proc/rlimit"
	"github.com/kitsunium/sdk/pkg/v1/proc/signal"
	"github.com/kitsunium/sdk/pkg/v1/proc/systemd/listen"
	"github.com/kitsunium/sdk/pkg/v1/proc/systemd/notify"
)

// backendProbe asks one capability's engine for the smallest thing that
// capability does, on this test's own process, a child it spawns, or a path
// under its own temporary directory, and returns the engine's answer. skip,
// when not empty, says why this host cannot answer for its GOOS.
type backendProbe func(t *testing.T) (skip string, err error)

// spawnSelf starts this test binary running no test, as the one child a probe
// needs, and waits for it.
func spawnSelf(t *testing.T, spec process.Spec) error {
	t.Helper()
	spec.Path = os.Args[0]
	spec.Args = []string{os.Args[0], "-test.run=^$"}
	spec.Stdio = process.StdioNull
	p, err := process.Start(context.Background(), spec)
	//: refused before or at the spawn.
	if err != nil {
		return err
	}
	_, err = p.Wait()
	return err
}

// spawnPTY starts this test binary running no test on a pseudo-terminal of
// its own, and waits for it.
func spawnPTY(t *testing.T) error {
	t.Helper()
	p, err := process.Start(context.Background(), process.Spec{
		Path:  os.Args[0],
		Args:  []string{os.Args[0], "-test.run=^$"},
		Stdio: process.StdioPTY,
	})
	//: refused before or at the spawn.
	if err != nil {
		return err
	}
	_, err = p.Wait()
	return err
}

// backendProbes is one probe per Capability: what its engine answers here.
var backendProbes = map[proc.Capability]backendProbe{
	proc.CapProcessSpawn: func(t *testing.T) (string, error) {
		return "", spawnSelf(t, process.Spec{})
	},
	proc.CapRlimit: func(*testing.T) (string, error) {
		//: no limit to set: the engine answers for its platform alone.
		return "", rlimit.Apply(0, map[rlimit.Resource]rlimit.Limit{})
	},
	proc.CapUmaskNice: func(t *testing.T) (string, error) {
		umask := 0o022
		return "", spawnSelf(t, process.Spec{Umask: &umask})
	},
	proc.CapOOMScoreAdj: func(t *testing.T) (string, error) {
		adj := 0
		//: a refusal of the VALUE (a raised bias lowered without privilege) is
		//: still the engine taking the knob: only UNSUPPORTED_PLATFORM is not.
		return "", spawnSelf(t, process.Spec{OOMScoreAdj: &adj})
	},
	proc.CapCgroup: func(t *testing.T) (string, error) {
		//: FreeBSD's backend needs a kernel built and booted with RACCT.
		if runtime.GOOS == "freebsd" && !cgroup.Available() {
			return "the FreeBSD kernel carries no RACCT (kern.racct.enable=0): cgroup.Available says so, the matrix is per GOOS", nil
		}
		//: a directory of this test's own, never the system hierarchy.
		g, err := cgroup.Create("probe", cgroup.WithRoot(t.TempDir()))
		//: a Job Object or rctl group is released at once.
		if g != nil {
			_ = g.Delete()
		}
		return "", err
	},
	proc.CapReaper: func(*testing.T) (string, error) {
		r := reaper.New()
		//: a reaper that never started stops at once; only the refusal counts.
		_, err := r.ReapOnce()
		return "", err
	},
	proc.CapSignalRelay: func(*testing.T) (string, error) {
		src := make(chan signal.Signal)
		close(src)
		//: nothing to deliver: no signal reaches anyone.
		return "", signal.Relay(src, signal.Target(os.Getpid()))
	},
	proc.CapSdNotify: func(t *testing.T) (string, error) {
		//: a socket nobody listens on, under this test's own directory.
		t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "notify.sock"))
		return "", notify.Ready()
	},
	proc.CapSocketActivation: func(t *testing.T) (string, error) {
		t.Setenv("LISTEN_PID", "")
		t.Setenv("LISTEN_FDS", "")
		_, err := listen.Files(false)
		return "", err
	},
	proc.CapDieWithParent: func(t *testing.T) (string, error) {
		//: the child is this test binary running no test: it exits at once.
		return "", spawnSelf(t, process.Spec{DieWithParent: process.SIGTERM})
	},
	proc.CapScheduling: func(t *testing.T) (string, error) {
		//: SCHED_IDLE on Linux, rtprio's idle class on FreeBSD; a refusal of the
		//: VALUE (an unprivileged idle class) is still the engine taking the knob.
		return "", spawnSelf(t, process.Spec{Scheduling: &process.Scheduling{Policy: process.SchedIdle}})
	},
	proc.CapPTY: func(t *testing.T) (string, error) {
		return "", spawnPTY(t)
	},
	proc.CapSandbox: func(t *testing.T) (string, error) {
		//: no_new_privs alone: the smallest sandbox, no privilege needed.
		return "", spawnSelf(t, process.Spec{Sandbox: &process.Sandbox{NoNewPrivileges: true}})
	},
	proc.CapProcessHandle: func(*testing.T) (string, error) {
		//: this test's own process: a handle on it touches no one else.
		_, err := process.Open(os.Getpid())
		return "", err
	},
}

// TestSupportedIsTheEnginesAnswer holds the capability matrix to the engines
// (#941, ADR 0018 amended 2026-11-04): on the platform the test runs on,
// Supported(cap) is true exactly when the capability's engine does not answer
// UNSUPPORTED_PLATFORM. Every runtime lane of the SDK runs it, so a backend
// that ships, or a matrix row that promises one that does not, fails here.
func TestSupportedIsTheEnginesAnswer(t *testing.T) {
	//: not parallel — two probes set process-wide environment variables.
	for c, probe := range backendProbes {
		t.Run(c.String(), func(t *testing.T) {
			skip, err := probe(t)
			//: this host cannot answer for its GOOS; the reason says which.
			if skip != "" {
				t.Skip(skip)
			}
			unsupported, _ := perrs.CodeOf(proc.UnsupportedPlatform)
			engine := !perrs.HasCode(err, unsupported)
			//: the matrix and the engine say the same thing.
			if got := proc.Supported(c); got != engine {
				t.Fatalf("Supported(%s) on %s = %v, but the engine answered %v", c, runtime.GOOS, got, err)
			}
		})
	}
}

// capabilityScan bounds the values the probe table is checked over: far past
// the last capability, so one numbered apart from the block (CapOOMScoreAdj)
// is seen as well as the block's.
const capabilityScan proc.Capability = 64

// TestEveryCapabilityHasABackendProbe keeps the probe table whole: a
// capability added to the matrix without a probe here is a row nothing holds
// to its engine, and a probe for a value no capability holds is a row of
// nothing.
func TestEveryCapabilityHasABackendProbe(t *testing.T) {
	t.Parallel()
	named := 0
	//: every value String names, gaps skipped.
	for c := proc.Capability(0); c < capabilityScan; c++ {
		//: a value no capability holds.
		if c.String() == "Capability("+strconv.Itoa(int(c))+")" {
			continue
		}
		named++
		//: a named capability with no probe.
		if _, ok := backendProbes[c]; !ok {
			t.Errorf("capability %s has no probe in backendProbes", c)
		}
	}
	//: as many probes as capabilities: none for a value that names nothing.
	if named != len(backendProbes) {
		t.Errorf("%d capabilities are named but backendProbes holds %d probes", named, len(backendProbes))
	}
}
