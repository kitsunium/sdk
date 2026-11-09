//go:build !windows

package service_test

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The shell's part of the cgroup modes (S0g, kitsunium/sdk#734) over a fake
// group of the fake spawner's processes: the group's preparation, its signals
// — each process once —, its fresh reads, its watch and its end.

// fakeGroup is a CgroupUnit whose members are the fake spawner's live
// processes and the children the test adds; its watch publishes what the
// test sends on states.
type fakeGroup struct {
	sp       *fakeSpawner
	path     string
	states   chan coreproc.GroupEventsValue
	mu       sync.Mutex
	children []int
	stubborn bool
	oomKills int64
	watches  int
	pruned   int
	oomGroup bool
	kills    int
	sigs     []coreproc.Signal
	handed   [][2]int
}

func newFakeGroup(sp *fakeSpawner) *fakeGroup {
	return &fakeGroup{sp: sp, path: "/sys/fs/cgroup/test.slice/unit", states: make(chan coreproc.GroupEventsValue)}
}

// live are the spawner's processes not ended.
func (g *fakeGroup) live() []*fakeProc {
	g.sp.mu.Lock()
	defer g.sp.mu.Unlock()
	var out []*fakeProc
	for _, p := range g.sp.procs {
		p.mu.Lock()
		if !p.ended {
			out = append(out, p)
		}
		p.mu.Unlock()
	}
	return out
}

func (g *fakeGroup) Members() ([]int, error) {
	var pids []int
	for _, p := range g.live() {
		pids = append(pids, p.pid)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append(pids, g.children...), nil
}

func (g *fakeGroup) Contains(pid int) (bool, error) {
	members, _ := g.Members()
	return slices.Contains(members, pid), nil
}

func (g *fakeGroup) Kill() error {
	g.mu.Lock()
	g.kills++
	g.children = nil
	g.mu.Unlock()
	for _, p := range g.live() {
		p.end(coreproc.ExitValue{Code: -1, Signal: coreproc.Signal(syscall.SIGKILL), Signaled: true})
	}
	return nil
}

func (g *fakeGroup) Signal(sig coreproc.Signal) error {
	g.mu.Lock()
	g.sigs = append(g.sigs, sig)
	if syscall.Signal(sig) == syscall.SIGKILL || (syscall.Signal(sig) == syscall.SIGTERM && !g.stubborn) {
		g.children = nil
	}
	g.mu.Unlock()
	for _, p := range g.live() {
		_ = p.Signal(sig)
	}
	return nil
}

func (g *fakeGroup) Path() string { return g.path }

func (g *fakeGroup) Stat() (coreproc.GroupStatValue, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return coreproc.GroupStatValue{MemoryEvents: coreproc.MemoryEventsValue{OOMKill: g.oomKills}}, nil
}

func (g *fakeGroup) Watch(ctx context.Context) (<-chan coreproc.GroupEventsValue, error) {
	g.mu.Lock()
	g.watches++
	g.mu.Unlock()
	members, _ := g.Members()
	first := coreproc.GroupEventsValue{Populated: len(members) > 0}
	out := make(chan coreproc.GroupEventsValue)
	go func() {
		defer close(out)
		pending := []coreproc.GroupEventsValue{first}
		for {
			var send chan coreproc.GroupEventsValue
			var next coreproc.GroupEventsValue
			if len(pending) > 0 {
				send, next = out, pending[0]
			}
			select {
			case send <- next:
				pending = pending[1:]
			case v := <-g.states:
				pending = append(pending, v)
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (g *fakeGroup) Delegate(uid, gid int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.handed = append(g.handed, [2]int{uid, gid})
	return nil
}

func (g *fakeGroup) DeleteBeneath() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruned++
	return nil
}

func (g *fakeGroup) SetMemoryOOMGroup(enabled bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.oomGroup = enabled
	return nil
}

// add puts a child the main process forked in the group.
func (g *fakeGroup) add(pid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.children = append(g.children, pid)
}

// leave takes every child out of the group.
func (g *fakeGroup) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.children = nil
}

// publish hands the watch a state, as the kernel's change would.
func (g *fakeGroup) publish(v coreproc.GroupEventsValue) { g.states <- v }

// groupUnit builds a unit over the fake spawner and a fake group, on the
// system clock: nothing here waits for a timer.
func groupUnit(t *testing.T, cfg service.Config) (*service.Unit, *fakeSpawner, *fakeGroup) {
	t.Helper()
	sp := &fakeSpawner{}
	g := newFakeGroup(sp)
	cfg.Name, cfg.Spawner, cfg.Cgroup = "unit", sp, g
	if len(cfg.ExecStart) == 0 {
		cfg.ExecStart = []coresvc.CommandValue{command(t, "/bin/daemon")}
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = u.Close(ctx)
	})
	return u, sp, g
}

// TestGroupUnitPreparesSignalsAndCleans: the start's first spawn prepares the
// group — its watch, Delegate= to the Spec's user and group, memory.oom.group
// for OOMPolicy=kill — and goes into the group's directory; the stop
// (control-group, the default) signals the group — a main process that
// ignores SIGTERM gets it once, through the group, never also through its
// handle —, kills it through the group's Signal after TimeoutStopSec=, never
// through cgroup.kill, and removes what the service made beneath the group.
func TestGroupUnitPreparesSignalsAndCleans(t *testing.T) {
	cfg := service.Config{
		Delegate: true, OOMPolicy: coresvc.OOMPolicyKill, TimeoutStop: 50 * time.Millisecond,
		ExecStart: []coresvc.CommandValue{command(t, "/bin/stubborn")},
	}
	cfg.Spec.User, cfg.Spec.Group = "4242", "4343"
	u, sp, g := groupUnit(t, cfg)
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	g.mu.Lock()
	prepared := g.watches == 1 && slices.Equal(g.handed, [][2]int{{4242, 4343}}) && g.oomGroup
	g.mu.Unlock()
	if !prepared || sp.specs[0].CgroupPath != g.path {
		t.Fatalf("prepared %v (watches %d, handed %v, oom.group %v), CgroupPath %q", prepared, g.watches, g.handed, g.oomGroup, sp.specs[0].CgroupPath)
	}
	main := sp.last()
	g.add(77777)
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	terms := 0
	for _, sig := range main.sent() {
		if syscall.Signal(sig) == syscall.SIGTERM {
			terms++
		}
	}
	if terms != 1 {
		t.Fatalf("the main process got %v: want one SIGTERM, the group's", main.sent())
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pruned != 1 || g.kills != 0 || !slices.Contains(g.sigs, coreproc.Signal(syscall.SIGKILL)) || len(g.children) != 0 || u.State().State != coresvc.StateFailed {
		t.Fatalf("after the stop: pruned %d, cgroup.kill %d, group signals %v, children %v, state %s", g.pruned, g.kills, g.sigs, g.children, u.State().State)
	}
}

// TestLateEmptyNeverEndsAUnit: an ExitType=cgroup unit whose main process
// ended is up while a child is left; a watch state saying "empty" that
// arrives while the child is still there — read before, delivered late — is
// read again and changes nothing; the child's end, announced, stops the unit.
func TestLateEmptyNeverEndsAUnit(t *testing.T) {
	u, sp, g := groupUnit(t, service.Config{ExitType: coresvc.ExitTypeCgroup})
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub, cancel := u.Subscribe(64)
	defer cancel()
	g.add(77777)
	sp.last().end(coreproc.ExitValue{})
	waitMainGone(t, u)
	g.publish(coreproc.GroupEventsValue{})
	g.publish(coreproc.GroupEventsValue{Populated: true})
	if err := u.ResetFailed(ctx); err != nil {
		t.Fatal(err)
	}
	if st := u.State(); st.State != coresvc.StateActive {
		t.Fatalf("after a late empty state: %s, want active while the child runs", st.State)
	}
	g.leave()
	g.publish(coreproc.GroupEventsValue{})
	waitState(t, sub, coresvc.StateInactive)
	if r := u.State().Result; r != coresvc.ResultSuccess {
		t.Fatalf("result %s, want success", r)
	}
}

// waitMainGone polls until the unit has no main process.
func waitMainGone(t *testing.T, u *service.Unit) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for u.State().MainPID != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the main process is still the unit's: %+v", u.State())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestOOMKillFromTheWatch: an OOM kill counted before the unit started is no
// news; a new one, announced by the watch, stops the unit (OOMPolicy=stop,
// the default) with the result oom-kill.
func TestOOMKillFromTheWatch(t *testing.T) {
	u, _, g := groupUnit(t, service.Config{})
	g.oomKills = 3
	ctx := context.Background()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	sub, cancel := u.Subscribe(64)
	defer cancel()
	g.publish(coreproc.GroupEventsValue{Populated: true, MemoryEvents: coreproc.MemoryEventsValue{OOMKill: 3}})
	g.publish(coreproc.GroupEventsValue{Populated: true, MemoryEvents: coreproc.MemoryEventsValue{OOMKill: 3}})
	if err := u.ResetFailed(ctx); err != nil {
		t.Fatal(err)
	}
	if st := u.State(); st.State != coresvc.StateActive {
		t.Fatalf("after an old OOM kill: %s, want active", st.State)
	}
	g.mu.Lock()
	g.oomKills = 4
	g.mu.Unlock()
	g.publish(coreproc.GroupEventsValue{Populated: true, MemoryEvents: coreproc.MemoryEventsValue{OOMKill: 4}})
	ev := waitState(t, sub, coresvc.StateFailed)
	if ev.Result != coresvc.ResultOOMKill {
		t.Fatalf("result %s, want oom-kill", ev.Result)
	}
}

// TestGroupWatchLeavesNoGoroutine runs a unit with a watched group through
// starts and stops in a synctest bubble: the watch's forwarder ends with the
// unit, joined by Close.
func TestGroupWatchLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sp := &fakeSpawner{}
		g := newFakeGroup(sp)
		u, err := service.NewUnit(&service.Config{
			Name: "u", Spawner: sp, Cgroup: g, Delegate: true,
			ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for range 3 {
			if err := u.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := u.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if err := u.Close(ctx); err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.watches != 1 || g.pruned != 3 {
			t.Fatalf("watches %d, cleanups %d: want one watch, a cleanup per run", g.watches, g.pruned)
		}
	})
}
