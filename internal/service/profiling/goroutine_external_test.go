// Package profiling_test — goroutine dumps: the format, parsed from fixtures,
// and the live process, with labels printed in the headers and without.
package profiling_test

import (
	"context"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/profiling"
)

// dump is a goroutine dump with every construct the parser reads.
const dump = `goroutine 1 [running]:
main.main()
	/src/main.go:18 +0xf4

goroutine 35 [chan receive, 3 minutes] {kit_node: shop/endpoint/Get, "odd key": "va\"lue, x: y"}:
main.main.func1.1(0x1, {0x2, 0x3})
	/src/main.go:14 +0x24
created by main.main.func1 in goroutine 1
	/src/main.go:14 +0x68

goroutine 7 gp=0xc000002380 m=nil [select (scan), locked to thread]:
sync.(*Cond).Wait(...)
	C:/Program Files/Go/src/sync/cond.go:71
github.com/x/y.(*T)[...].Loop(0xc0001)
	C:/work/y/loop.go:120 +0x1d
...additional frames elided...
created by github.com/x/y.Start
	C:/work/y/start.go:9 +0x55

goroutine oops [running]:
main.lost()
	/src/lost.go:1

goroutine 9 [chan receive (nil chan)]:
goroutine running on other thread; stack unavailable
	/orphan/location.go:3

goroutine 11 [runnable]:
main.noargs(...)
	/src/noargs.go
`

// TestADumpParsesIntoGoroutines pins the format: ids, states with their flags
// stripped and their meaningful parentheses kept, the wait in minutes, the
// thread lock, quoted labels, frames with their files — a Windows drive
// included — the creator, and a header that does not parse skipped.
func TestADumpParsesIntoGoroutines(t *testing.T) {
	t.Parallel()
	gs := profiling.ParseGoroutines([]byte(dump))
	if len(gs) != 5 {
		t.Fatalf("%d goroutines: %+v", len(gs), gs)
	}
	if last := gs[4]; last.State != "runnable" || len(last.Stack) != 1 || last.Stack[0].File != "/src/noargs.go" || last.Stack[0].Line != 0 {
		t.Errorf("goroutine 11 = %+v", last)
	}
	first := gs[0]
	if first.ID != 1 || first.State != "running" || len(first.Stack) != 1 || first.Stack[0] != (profiling.FrameValue{Function: "main.main", File: "/src/main.go", Line: 18}) {
		t.Errorf("goroutine 1 = %+v", first)
	}
	labelled := gs[1]
	if labelled.State != "chan receive" || labelled.Waiting != 3*time.Minute || labelled.Labels["kit_node"] != "shop/endpoint/Get" || labelled.Labels["odd key"] != `va"lue, x: y` {
		t.Errorf("goroutine 35 = %+v", labelled)
	}
	if labelled.Stack[0].Function != "main.main.func1.1" || labelled.CreatedBy.Function != "main.main.func1" || labelled.Creator != 1 || labelled.CreatedBy.Line != 14 {
		t.Errorf("goroutine 35's frames = %+v, created by %+v", labelled.Stack, labelled.CreatedBy)
	}
	windows := gs[2]
	if windows.ID != 7 || windows.State != "select" || !windows.LockedToThread || len(windows.Stack) != 2 {
		t.Fatalf("goroutine 7 = %+v", windows)
	}
	if windows.Stack[0] != (profiling.FrameValue{Function: "sync.(*Cond).Wait", File: "C:/Program Files/Go/src/sync/cond.go", Line: 71}) ||
		windows.Stack[1].Function != "github.com/x/y.(*T)[...].Loop" || windows.CreatedBy.File != "C:/work/y/start.go" || windows.Creator != 0 {
		t.Errorf("goroutine 7's frames = %+v, created by %+v", windows.Stack, windows.CreatedBy)
	}
	if last := gs[3]; last.ID != 9 || last.State != "chan receive (nil chan)" || len(last.Stack) != 0 {
		t.Errorf("goroutine 9 = %+v", last)
	}
}

// TestLiveGoroutinesCarryTheirLabelsEitherWay blocks three labelled
// goroutines in a select and finds them, with the labels printed in the dump's
// headers and with the labels matched from the counted profile.
//
// Each case sets GODEBUG itself: once it changes at run time, the runtime
// reads an unset tracebacklabels as 0, not as its default.
func TestLiveGoroutinesCarryTheirLabelsEitherWay(t *testing.T) {
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			pprof.Do(context.Background(), pprof.Labels("node", "waiter"), func(context.Context) { parkInSelect(release) })
		})
	}
	defer func() {
		close(release)
		wg.Wait()
	}()
	for _, godebug := range []string{"tracebacklabels=1", "tracebacklabels=0"} {
		t.Run(godebug, func(t *testing.T) {
			t.Setenv("GODEBUG", godebug)
			var waiters []profiling.GoroutineValue
			deadline := time.Now().Add(10 * time.Second)
			for len(waiters) < 3 && time.Now().Before(deadline) {
				gs, err := profiling.Goroutines()
				if err != nil {
					t.Fatal(err)
				}
				waiters = slices.DeleteFunc(gs, func(g profiling.GoroutineValue) bool { return g.Labels["node"] != "waiter" })
				time.Sleep(10 * time.Millisecond)
			}
			if len(waiters) != 3 {
				t.Fatalf("found %d labelled waiters", len(waiters))
			}
			for _, g := range waiters {
				top := profiling.GroupGoroutines([]profiling.GoroutineValue{g}, profiling.GroupConfig{})[0].Top
				if g.State != "select" || !strings.HasSuffix(top, ".parkInSelect") || g.CreatedBy.Function == "" {
					t.Errorf("a waiter = %+v, top %q", g, top)
				}
			}
		})
	}
}

// parkInSelect waits in a select until release is closed.
//
//go:noinline
func parkInSelect(release chan struct{}) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	select {
	case <-release:
	case <-tick.C:
	}
}
