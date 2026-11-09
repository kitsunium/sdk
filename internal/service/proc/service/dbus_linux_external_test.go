//go:build linux

package service_test

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The shell's part of Type=dbus (S0e, kitsunium/sdk#737) over a fake
// BusWatcher: the watch begun before the main process, the owners it
// announces, its refusal, its end, and its goroutines joined by Close — each
// test in a synctest bubble, where time is the bubble's and every goroutine
// the unit started must be gone when it ends.

// fakeBus is a BusWatcher whose name's owner the test sets: a watch holds
// the owner when it began, then every owner the test announces; lose ends
// every watch as a lost connection does.
type fakeBus struct {
	refuse  error
	owner   string
	names   []string
	watches []*fakeWatch
	mu      sync.Mutex
}

// fakeWatch is one watch's announcements and its lost connection.
type fakeWatch struct {
	in   chan string
	lost chan struct{}
}

func (b *fakeBus) WatchName(ctx context.Context, name string) (<-chan string, error) {
	b.mu.Lock()
	b.names = append(b.names, name)
	if b.refuse != nil {
		b.mu.Unlock()
		return nil, b.refuse
	}
	w := &fakeWatch{in: make(chan string, 16), lost: make(chan struct{})}
	b.watches = append(b.watches, w)
	first := b.owner
	b.mu.Unlock()
	out := make(chan string)
	go func() {
		defer close(out)
		pending := []string{first}
		for {
			var send chan string
			var next string
			if len(pending) > 0 {
				send, next = out, pending[0]
			}
			select {
			case send <- next:
				pending = pending[1:]
			case o := <-w.in:
				pending = append(pending, o)
			case <-w.lost:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// announce sets the name's owner, announced to every watch.
func (b *fakeBus) announce(owner string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owner = owner
	for _, w := range b.watches {
		w.in <- owner
	}
}

// lose ends every watch, as the connection to the bus going does.
func (b *fakeBus) lose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, w := range b.watches {
		close(w.lost)
	}
	b.watches = nil
}

// watched is how many watches were asked for, and the names they named.
func (b *fakeBus) watched() (int, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.names), append([]string(nil), b.names...)
}

// dbusUnit is a Type=dbus unit over the fake spawner and bus, on the
// bubble's clock.
func dbusUnit(t *testing.T, bus *fakeBus, edit func(*service.Config)) (*service.Unit, *fakeSpawner) {
	t.Helper()
	sp := &fakeSpawner{}
	cfg := service.Config{
		Name: "u", Spawner: sp, Type: coresvc.TypeDbus, BusName: "org.example.Svc", BusWatcher: bus,
		ExecStart: []coresvc.CommandValue{command(t, "/bin/daemon")},
	}
	if edit != nil {
		edit(&cfg)
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("NewUnit: %v", err)
	}
	return u, sp
}

// startAsync posts a start and waits until the bubble is idle: the main
// process spawned, the start waiting for the name.
func startAsync(u *service.Unit) chan error {
	done := make(chan error, 1)
	go func() { done <- u.Start(context.Background()) }()
	synctest.Wait()
	return done
}

// TestBusWatcherReadyAndLost: the watch begins before the main process,
// naming BusName=; the start waits until the bus announces an owner, then
// answers; the name announced lost stops the unit — the main process sent
// KillSignal= — with the result success.
func TestBusWatcherReadyAndLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := &fakeBus{}
		u, sp := dbusUnit(t, bus, nil)
		done := startAsync(u)
		if st := u.State(); st.State != coresvc.StateActivating || st.Sub != coresvc.SubStart || st.MainPID == 0 {
			t.Fatalf("before the name: %s/%s, main %d", st.State, st.Sub, st.MainPID)
		}
		select {
		case err := <-done:
			t.Fatalf("the start answered %v before the name was owned", err)
		default:
		}
		if n, names := bus.watched(); n != 1 || names[0] != "org.example.Svc" {
			t.Fatalf("watches %d %v, want one on BusName=", n, names)
		}
		bus.announce(":1.7")
		if err := <-done; err != nil {
			t.Fatalf("Start: %v", err)
		}
		if st := u.State(); st.State != coresvc.StateActive || st.Sub != coresvc.SubRunning {
			t.Fatalf("with the name: %s/%s", st.State, st.Sub)
		}
		main := sp.last()
		bus.announce("")
		synctest.Wait()
		st := u.State()
		if st.State != coresvc.StateInactive || st.Result != coresvc.ResultSuccess {
			t.Fatalf("after the name was lost: %s, result %s", st.State, st.Result)
		}
		if got := main.sent(); len(got) == 0 || syscall.Signal(got[0]) != syscall.SIGTERM {
			t.Fatalf("the main process got %v, want SIGTERM", got)
		}
		if err := u.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestBusWatchRefusedFailsTheStart: a watch the BusWatcher refuses is the
// start's first spawn failing — the result resources, nothing spawned —, as
// systemd's unit fails whose name it cannot watch.
func TestBusWatchRefusedFailsTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := &fakeBus{refuse: coreproc.UnsupportedPlatform}
		u, sp := dbusUnit(t, bus, nil)
		err := u.Start(context.Background())
		if !errs.HasCode(err, coresvc.CodeStartFailed) {
			t.Fatalf("Start: %v, want START_FAILED", err)
		}
		if st := u.State(); st.Result != coresvc.ResultResources || sp.count() != 0 {
			t.Fatalf("result %s, %d spawned: want resources and nothing spawned", st.Result, sp.count())
		}
		if err := u.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestBusOwnedBeforeTheSpawnTimesOut: a name some other process owned when
// the watch began is no readiness: the start fails after TimeoutStartSec=
// with the result timeout.
func TestBusOwnedBeforeTheSpawnTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := &fakeBus{owner: ":1.3"}
		u, _ := dbusUnit(t, bus, func(c *service.Config) { c.TimeoutStart = 5 * time.Second })
		err := u.Start(context.Background())
		if !errs.HasCode(err, coresvc.CodeStartFailed) || u.State().Result != coresvc.ResultTimeout {
			t.Fatalf("Start: %v (result %s), want START_FAILED by timeout", err, u.State().Result)
		}
		if err := u.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

// TestBusLostConnectionKeepsTheUnit: the watch's end — the connection to the
// bus lost — leaves the unit running; the next start watches again.
func TestBusLostConnectionKeepsTheUnit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := &fakeBus{}
		u, _ := dbusUnit(t, bus, nil)
		done := startAsync(u)
		bus.announce(":1.7")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		bus.lose()
		synctest.Wait()
		if st := u.State(); st.State != coresvc.StateActive {
			t.Fatalf("after the connection went: %s, want active", st.State)
		}
		ctx := context.Background()
		if err := u.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		bus.announce("")
		done = startAsync(u)
		bus.announce(":1.9")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if n, _ := bus.watched(); n != 2 {
			t.Fatalf("watches %d, want the next start to watch again", n)
		}
		if err := u.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// TestBusWatchLeavesNoGoroutine runs a Type=dbus unit through starts and
// stops in a synctest bubble: one watch for the unit's life, its forwarder
// and the watcher's goroutine ended by Close.
func TestBusWatchLeavesNoGoroutine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bus := &fakeBus{}
		u, _ := dbusUnit(t, bus, func(c *service.Config) { c.Restart = coresvc.RestartOnFailure })
		_, cancel := u.Subscribe(4)
		ctx := context.Background()
		for range 3 {
			done := startAsync(u)
			bus.announce(":1.7")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := u.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			bus.announce("")
		}
		if err := u.Close(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		if n, _ := bus.watched(); n != 1 {
			t.Fatalf("watches %d, want one for the unit's life", n)
		}
	})
}
