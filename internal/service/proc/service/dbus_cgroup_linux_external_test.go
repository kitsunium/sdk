//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The junction of S0e (#737, Type=dbus) with S0g (#734, the unit's group),
// against a real dbus-daemon the test launches under t.TempDir() (testBus):
// each suite tested the engine without the other. The unit's group is made
// under a delegated root (liveUnitGroup); the test skips without one or
// without dbus-daemon. Only the test's own processes are signalled.

// junctionBusUnit is a Type=dbus unit whose main process, this test binary,
// owns name on the bus at address and gives it back on SIGUSR1; spec's Env is
// set here, its other fields the caller's; each edit then sets directives.
func junctionBusUnit(t *testing.T, conn coresvc.BusWatcher, address, name string, spec coreproc.Spec, cg coresvc.CgroupKill, edits ...func(*service.Config)) *service.Unit {
	t.Helper()
	spec.Env = helperEnviron("dbus", busAddressEnv+"="+address, busNameEnv+"="+name)
	cfg := service.Config{
		Type: coresvc.TypeDbus, BusName: name, BusWatcher: conn, Cgroup: cg,
		TimeoutStart: 20 * time.Second, TimeoutStop: 10 * time.Second,
		Spec:      spec,
		ExecStart: []coresvc.CommandValue{command(t, os.Args[0], "-test.run=^$")},
	}
	for _, edit := range edits {
		edit(&cfg)
	}
	return realUnit(t, cfg)
}

// upOnItsName starts u and returns its main process once the unit is up on
// name, which the bus says is owned.
func upOnItsName(ctx context.Context, t *testing.T, u *service.Unit, conn interface {
	NameOwner(context.Context, string) (string, error)
}, name string,
) int {
	t.Helper()
	if err := u.Start(ctx); err != nil {
		st := u.State()
		t.Fatalf("Start: %v (result %s, main process %+v)", err, st.Result, st.MainExit)
	}
	st := u.State()
	if st.State != coresvc.StateActive || st.Sub != coresvc.SubRunning || st.MainPID <= 0 {
		t.Fatalf("after Start: %s/%s main %d", st.State, st.Sub, st.MainPID)
	}
	if owner, err := conn.NameOwner(ctx, name); err != nil || owner == "" {
		t.Fatalf("the name's owner: %q %v", owner, err)
	}
	return st.MainPID
}

// stoppedOnTheLoss tells main to give its name back and waits for the unit to
// stop on the loss the bus announces, with the result success.
func stoppedOnTheLoss(t *testing.T, u *service.Unit, sub <-chan coresvc.EventValue, main int) {
	t.Helper()
	gen := u.State().Generation
	if err := syscall.Kill(main, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	for {
		ev := waitState(t, sub, coresvc.StateInactive)
		if ev.Generation <= gen {
			continue
		}
		if ev.Result != coresvc.ResultSuccess {
			t.Fatalf("after the name was given back: result %s, want success", ev.Result)
		}
		return
	}
}

// emptied waits up to five seconds for g to hold no process.
func emptied(t *testing.T, g coreproc.Group) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		members, err := g.(coreproc.GroupReader).Members()
		if err == nil && len(members) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the unit's group still holds %v (%v) after the unit stopped", members, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLiveTypeDbusInItsGroup: a Type=dbus unit with a CgroupUnit (S0g) — both
// watches, the group's and the name's, armed by a start's first spawn — is up
// once its main process, spawned into the group, owns the name; the name
// given back stops the unit and empties the group. Twice: the second start
// finds both watches running and neither flag lost.
func TestLiveTypeDbusInItsGroup(t *testing.T) {
	g := liveUnitGroup(t)
	cg := liveGroupUnit(t, g)
	address, conn := testBus(t)
	const name = "org.kitsunium.Test.Grouped"
	u := junctionBusUnit(t, conn, address, name, coreproc.Spec{}, cg)
	sub, cancelSub := u.Subscribe(256)
	defer cancelSub()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for round := 1; round <= 2; round++ {
		main := upOnItsName(ctx, t, u, conn, name)
		if in, err := cg.Contains(main); err != nil || !in {
			t.Fatalf("round %d: Contains(%d) = %v, %v: not spawned into the unit's group", round, main, in, err)
		}
		stoppedOnTheLoss(t, u, sub, main)
		emptied(t, g)
	}
}
