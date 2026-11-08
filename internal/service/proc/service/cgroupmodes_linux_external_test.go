//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The cgroup modes (S0g, kitsunium/sdk#734) over real processes and the
// engine's fake cgroup: markUnit, whose members are the processes carrying
// the test's mark. It signals and kills nothing but them, and its watch
// polls their population.

// markPoll is how often markUnit's watch reads the marked processes.
const markPoll time.Duration = 5 * time.Millisecond

// markUnit is a CgroupUnit whose members are the live processes carrying
// mark — this test's descendants, the only ones it may signal.
type markUnit struct {
	mark  string
	mu    sync.Mutex
	kills int
}

func (u *markUnit) Kill() error {
	u.mu.Lock()
	u.kills++
	u.mu.Unlock()
	return u.Signal(coreproc.Signal(syscall.SIGKILL))
}

func (u *markUnit) Signal(sig coreproc.Signal) error {
	for _, pid := range marked(u.mark) {
		_ = syscall.Kill(pid, syscall.Signal(sig))
	}
	return nil
}

func (u *markUnit) Contains(pid int) (bool, error) { return slices.Contains(marked(u.mark), pid), nil }

func (u *markUnit) Members() ([]int, error) { return marked(u.mark), nil }

func (*markUnit) Path() string { return "" }

func (*markUnit) Stat() (coreproc.GroupStatValue, error) { return coreproc.GroupStatValue{}, nil }

func (*markUnit) Delegate(int, int) error { return nil }

func (*markUnit) DeleteBeneath() error { return nil }

func (*markUnit) SetMemoryOOMGroup(bool) error { return nil }

// Watch polls the marked processes and publishes each change of their
// population, the latest in a one-slot channel, until ctx ends.
func (u *markUnit) Watch(ctx context.Context) (<-chan coreproc.GroupEventsValue, error) {
	out := make(chan coreproc.GroupEventsValue, 1)
	last := coreproc.GroupEventsValue{Populated: len(marked(u.mark)) > 0}
	out <- last
	go func() {
		defer close(out)
		tick := time.NewTicker(markPoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			now := coreproc.GroupEventsValue{Populated: len(marked(u.mark)) > 0}
			if now == last {
				continue
			}
			last = now
			select {
			case <-out:
			default:
			}
			out <- now
		}
	}()
	return out, nil
}

// forkingChild is a main process that forks a child, writes its PID, and
// waits for it: SIGTERM ends the shell, not the child.
func forkingChild(t *testing.T, pidFile string) coresvc.CommandValue {
	t.Helper()
	return command(t, ":/bin/sh", "-c", `sleep 60 & echo $! > '`+pidFile+`'; wait`)
}

// waitFile waits for a script to write path.
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing at %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// alive reports whether pid is a live process carrying mark.
func alive(mark string, pid int) bool {
	return slices.Contains(marked(mark), pid)
}

// TestRealKillModesWithAForkingChild: each KillMode= over a main process that
// forked a child. process ends the main process alone, the child left; none
// leaves both; control-group (the default with a CgroupUnit) and mixed leave
// no survivor once Stop returned.
func TestRealKillModesWithAForkingChild(t *testing.T) {
	for _, tc := range []struct {
		name                string
		mode                coresvc.KillMode
		mainLeft, childLeft bool
	}{
		{"process", coresvc.KillModeProcess, false, true},
		{"none", coresvc.KillModeNone, true, true},
		{"default", coresvc.KillModeDefault, false, false},
		{"control-group", coresvc.KillModeControlGroup, false, false},
		{"mixed", coresvc.KillModeMixed, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mark := markOf(t)
			pidFile := filepath.Join(t.TempDir(), "child")
			u := realUnit(t, service.Config{
				KillMode: tc.mode, Cgroup: &markUnit{mark: mark}, TimeoutStop: 10 * time.Second,
				Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin", mark}},
				ExecStart: []coresvc.CommandValue{forkingChild(t, pidFile)},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := u.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			main := u.State().MainPID
			waitFile(t, pidFile)
			child := readPID(t, pidFile)
			if err := u.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if got := alive(mark, main); got != tc.mainLeft {
				t.Errorf("main process left: %v, want %v", got, tc.mainLeft)
			}
			if got := alive(mark, child); got != tc.childLeft {
				t.Errorf("forked child left: %v, want %v", got, tc.childLeft)
			}
		})
	}
}

// TestRealExitTypeCgroup: a main process that exits at once, leaving a child
// for a while, leaves the unit active without a main process — not ended
// before the last process leaves —, then inactive once the child is gone.
func TestRealExitTypeCgroup(t *testing.T) {
	mark := markOf(t)
	u := realUnit(t, service.Config{
		ExitType: coresvc.ExitTypeCgroup, Cgroup: &markUnit{mark: mark},
		Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin", mark}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", "sleep 0.5 & exit 0")},
	})
	sub, cancel := u.Subscribe(64)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	upAlone := false
	for {
		select {
		case ev := <-sub:
			if ev.State == coresvc.StateActive && ev.MainPID == 0 && ev.MainExited {
				upAlone = len(marked(mark)) > 0
			}
			if ev.State == coresvc.StateInactive {
				if !upAlone {
					t.Fatalf("the unit ended without being up on its child alone: %+v", ev)
				}
				if left := marked(mark); len(left) != 0 {
					t.Fatalf("inactive with %v still running", left)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("no end: %+v", u.State())
		}
	}
}

// TestRealForkingEndsWithItsCgroup: a forking unit whose main process is not
// guessed (GuessMainPID=no) is up while its group has members, and ends when
// the daemon it left is gone — which only a stop did before S0g.
func TestRealForkingEndsWithItsCgroup(t *testing.T) {
	mark := markOf(t)
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	u := forkingUnit(t, mark, service.Config{
		GuessMainPID: coresvc.ToggleNo, Cgroup: &markUnit{mark: mark},
		ExecStart: []coresvc.CommandValue{daemonize(t, `echo $$ > "`+pidFile+`"; exec sleep 0.5`, pidFile)},
	})
	sub, cancel := u.Subscribe(64)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := u.State(); st.State != coresvc.StateActive || st.MainPID != 0 {
		t.Fatalf("after Start: %s main %d, want active on the group alone", st.State, st.MainPID)
	}
	waitState(t, sub, coresvc.StateInactive)
}
