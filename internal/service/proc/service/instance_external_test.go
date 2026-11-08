//go:build !windows

package service_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The shell of the instances per connection (S0f, kitsunium/sdk#740), over
// the fake spawner: what each instance's processes are handed, its end, its
// refusals, and a thousand of them leaving nothing behind.

// connection returns one end of a pipe standing for an accepted connection,
// and the other end, closed at the test's end.
func connection(t *testing.T) (conn, peer *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return w, r
}

// ended waits for an instance's end: its subscription closes.
func ended(t *testing.T, u *service.Unit) {
	t.Helper()
	sub, _ := u.Subscribe(1)
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for {
		select {
		case _, ok := <-sub:
			if !ok {
				return
			}
		case <-guard.C:
			t.Fatalf("instance %s still running: %+v", u.State().Name, u.State())
		}
	}
}

// closedFile reports whether f's descriptor was closed.
func closedFile(f *os.File) bool {
	_, err := f.Stat()
	return err != nil
}

// envHas reports whether env holds assignment.
func envHas(env []string, assignment string) bool {
	return slices.Contains(env, assignment)
}

// TestInstancesEachTheirConnection: three connections, three instances, each
// its own state and its own connection — at fd 3 of its ExecStart= alone,
// LISTEN_FDS=1, named "connection", LISTEN_PID set after the fork —, none to
// ExecStartPre=.
func TestInstancesEachTheirConnection(t *testing.T) {
	sp := &fakeSpawner{}
	template := service.Config{
		Name: "template", Spawner: sp,
		ExecStartPre: []coresvc.CommandValue{command(t, "/bin/true")},
		ExecStart:    []coresvc.CommandValue{command(t, "/bin/daemon")},
	}
	var units []*service.Unit
	var conns []*os.File
	for i := range 3 {
		conn, _ := connection(t)
		u, err := service.SpawnInstance(context.Background(), &template, "conn-"+strconv.Itoa(i), conn)
		if err != nil {
			t.Fatalf("SpawnInstance %d: %v", i, err)
		}
		t.Cleanup(func() { _ = u.Close(context.Background()) })
		units, conns = append(units, u), append(conns, conn)
	}
	for i, u := range units {
		if st := u.State(); st.Name != "conn-"+strconv.Itoa(i) || st.State != coresvc.StateActive {
			t.Errorf("instance %d: %s %s", i, st.Name, st.State)
		}
	}
	sp.mu.Lock()
	specs := slices.Clone(sp.specs)
	sp.mu.Unlock()
	if len(specs) != 6 {
		t.Fatalf("%d spawns, want 3 ExecStartPre= and 3 ExecStart=", len(specs))
	}
	for i := range 3 {
		pre, main := specs[2*i], specs[2*i+1]
		if len(pre.ExtraFiles) != 0 || envHas(pre.Env, "LISTEN_FDS=1") {
			t.Errorf("instance %d: ExecStartPre= was handed %v", i, pre.ExtraFiles)
		}
		if len(main.ExtraFiles) != 1 || main.ExtraFiles[0] != conns[i] {
			t.Errorf("instance %d: ExecStart= was handed %v, want its connection alone", i, main.ExtraFiles)
		}
		if !envHas(main.Env, "LISTEN_FDS=1") || !envHas(main.Env, "LISTEN_FDNAMES=connection") || !slices.Contains(main.PIDEnv, "LISTEN_PID") {
			t.Errorf("instance %d: env %v, PIDEnv %v", i, main.Env, main.PIDEnv)
		}
	}
	if template.Name != "template" || len(template.Spec.ExtraFiles) != 0 {
		t.Fatalf("the template was written to: %+v", template)
	}
}

// TestInstanceEndsWithItsProcess: once its process is gone the instance
// ends on its own — its connection closed, its subscriptions closed, every
// later call UNIT_CLOSED.
func TestInstanceEndsWithItsProcess(t *testing.T) {
	sp := &fakeSpawner{}
	conn, _ := connection(t)
	u, err := service.SpawnInstance(context.Background(), &service.Config{Spawner: sp, ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")}}, "c1", conn)
	if err != nil {
		t.Fatal(err)
	}
	if closedFile(conn) {
		t.Fatal("the connection was closed while the instance ran")
	}
	sp.last().end(coreproc.ExitValue{})
	ended(t, u)
	if !closedFile(conn) {
		t.Fatal("the instance ended and its connection is open")
	}
	if err = u.Start(context.Background()); !errs.HasCode(err, coresvc.CodeUnitClosed) {
		t.Fatalf("Start after the end: %v, want UNIT_CLOSED", err)
	}
	if err = u.Close(context.Background()); err != nil {
		t.Fatalf("Close after the end: %v", err)
	}
}

// TestInstanceStartFailure: a start that fails returns START_FAILED and the
// instance — ended, its connection closed, its result in State.
func TestInstanceStartFailure(t *testing.T) {
	conn, _ := connection(t)
	u, err := service.SpawnInstance(context.Background(), &service.Config{
		Spawner: &fakeSpawner{}, ExecStart: []coresvc.CommandValue{command(t, "/bin/unspawnable")},
	}, "c1", conn)
	if !errs.HasCode(err, coresvc.CodeStartFailed) || u == nil {
		t.Fatalf("SpawnInstance = %v, %v; want the instance and START_FAILED", u, err)
	}
	ended(t, u)
	if st := u.State(); st.State != coresvc.StateFailed || st.Result != coresvc.ResultResources {
		t.Fatalf("after the failure: %s %s", st.State, st.Result)
	}
	if !closedFile(conn) {
		t.Fatal("a failed instance left its connection open")
	}
}

// TestInstanceCanceledStillRuns: a caller whose context is canceled already
// gets its error and the instance, which starts all the same and ends with
// its process — the connection closed.
func TestInstanceCanceledStillRuns(t *testing.T) {
	conn, _ := connection(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u, err := service.SpawnInstance(ctx, &service.Config{Spawner: &fakeSpawner{}, ExecStart: []coresvc.CommandValue{command(t, "/bin/true")}}, "c1", conn)
	if err != context.Canceled || u == nil {
		t.Fatalf("SpawnInstance(canceled) = %v, %v; want the instance and context.Canceled", u, err)
	}
	ended(t, u)
	if !closedFile(conn) || u.State().Restarts != 0 || u.State().Generation == 0 {
		t.Fatalf("the instance did not run to its end: %+v", u.State())
	}
}

// TestInstanceRefusals: no connection is CONFIG_INVALID, a configuration
// NewUnit refuses is refused here too — no unit either way, the connection
// and the template's NotifySource closed.
func TestInstanceRefusals(t *testing.T) {
	src := &closingNotify{}
	_, err := service.SpawnInstance(context.Background(), &service.Config{Notify: src, ExecStart: []coresvc.CommandValue{command(t, "/bin/d")}}, "c", nil)
	if !errs.HasCode(err, coresvc.CodeConfigInvalid) || !src.closed {
		t.Fatalf("no connection: %v, source closed %v", err, src.closed)
	}
	conn, _ := connection(t)
	u, err := service.SpawnInstance(context.Background(), &service.Config{}, "c", conn)
	if !errs.HasCode(err, coresvc.CodeConfigInvalid) || u != nil || !closedFile(conn) {
		t.Fatalf("no ExecStart=: %v, %v, connection closed %v", u, err, closedFile(conn))
	}
}

// closingNotify is a NotifySource that records its Close.
type closingNotify struct{ closed bool }

func (*closingNotify) Socket() string { return "@closing" }

func (*closingNotify) Recv(ctx context.Context) (coresvc.NotifyValue, error) {
	<-ctx.Done()
	return coresvc.NotifyValue{}, ctx.Err()
}

func (c *closingNotify) Close() error {
	c.closed = true
	return nil
}

// TestInstancesLeakNothing: a thousand instances, each started and ended
// with its process, leave no goroutine behind — the bubble fails on one still
// blocked — and, on Linux, the descriptor table as they found it: taken
// settled before the bubble, compared once every goroutine of it has ended.
func TestInstancesLeakNothing(t *testing.T) {
	before := settledDescriptors(t)
	synctest.Test(t, func(t *testing.T) {
		sp := &fakeSpawner{}
		template := service.Config{
			Spawner: sp, Clock: clock.NewManualClock(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)),
			ExecStart: []coresvc.CommandValue{command(t, "/bin/true")},
		}
		for i := range 1000 {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			u, err := service.SpawnInstance(context.Background(), &template, "c"+strconv.Itoa(i), w)
			if err != nil {
				t.Fatalf("instance %d: %v", i, err)
			}
			ended(t, u)
			if !closedFile(w) {
				t.Fatalf("instance %d left its connection open", i)
			}
			_ = r.Close()
		}
	})
	sameDescriptors(t, "a thousand instances", before, descriptors(t))
}
