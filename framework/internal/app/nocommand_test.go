package app_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
)

// daemon is what a running app keeps going: its lifecycle components, its
// loops, and its loops' goroutines — the HTTP listener's, which follow the
// connections a test opens, aside.
type daemon struct {
	components []string
	loops      []string
	goroutines int
}

// daemonOf reads what app keeps going, once it has settled.
//
// Start returns once every loop's goroutine is started, not once it has
// run: a goroutine takes its loop's label (asLoop) when it first runs, and
// one that has not run yet is in no loop's group; a consumer's second
// goroutine, its pump, does not even exist until the first has run. Read
// right after Start, the count depended on the scheduler — two of four on a
// Windows runner. So the goroutines are read once the daemon has settled:
// every loop that owns a goroutine (all but a job, which runs on the
// scheduler's, and the HTTP listener) shows one, and none of the loops'
// goroutines is running, runnable or waiting on a lock — each is parked
// where it waits for its next wake, after starting what it starts.
func daemonOf(t *testing.T, a *app.App) daemon {
	t.Helper()
	var d daemon
	g := a.Graph()
	owners := map[string]bool{}
	for _, c := range g.Runtime.Components {
		d.components = append(d.components, c.Name)
	}
	for _, l := range g.Runtime.Loops {
		d.loops = append(d.loops, l.Name)
		if l.Kind != model.LoopJob && l.Kind != model.LoopHTTP {
			owners[l.Name] = true
		}
	}
	var last model.Goroutines
	deadline := time.Now().Add(10 * time.Second)
	for {
		call(t, a, "GET /_app/api/goroutines", noBody).json(t, &last)
		if daemonSettled(&last, owners) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon did not settle: loops owning a goroutine %v, goroutines %+v", owners, last.Groups)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, grp := range last.Groups {
		if grp.Loop != "" && grp.Loop != "http" {
			d.goroutines += grp.Count
		}
	}
	slices.Sort(d.components)
	slices.Sort(d.loops)
	return d
}

// daemonSettled reports whether every loop of owners shows a goroutine in gs and
// every loop's goroutine is parked, none running, runnable or on a lock.
func daemonSettled(gs *model.Goroutines, owners map[string]bool) bool {
	seen := map[string]bool{}
	for _, grp := range gs.Groups {
		if grp.Loop == "" || grp.Loop == "http" {
			continue
		}
		state := strings.ToLower(grp.State)
		if state == "running" || state == "runnable" || state == "syscall" ||
			strings.Contains(state, "mutex") || strings.Contains(state, "semacquire") {
			return false
		}
		seen[grp.Loop] = true
	}
	for name := range owners {
		if !seen[name] {
			return false
		}
	}
	return true
}

// stopNow stops app, which a test started, before the next start.
func stopNow(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// synchronous declares commands and queries that are not queued: one keyed,
// one authorized, one exposed.
func synchronous() *app.Service {
	s := app.NewService("inline", "Commands and queries a caller runs itself.")
	echo := func(_ context.Context, in LabInput) (LabInput, error) { return in, nil }
	s.Command("keyed", echo).Key(func(in LabInput) string { return in.Key })
	s.Command("ruled", echo).Authorize(func(context.Context, LabInput) error { return nil })
	s.Query("asked", echo).Expose("POST /inline/asked", app.Anyone())
	return s
}

// A product that declares no command — the shop of kit's own tests — pays
// nothing for them: its graph says nothing of them, so its revision is what
// it was; and commands and queries that are not queued add no lifecycle
// component, no loop and no goroutine to it (ADR 0005).
func TestAProductWithoutCommandsPaysNothing(t *testing.T) {
	shop := startApp(t, []*app.Service{Shop, Audit})
	g := shop.Graph()
	raw, err := json.Marshal(struct {
		Nodes []model.Node
		Edges []model.Edge
	}{g.Nodes, g.Edges})
	if err != nil {
		t.Fatal(err)
	}
	for _, said := range []string{`"command":`, `"query":`, `"readModel":`, `"exposes":`, `"dispatches"`, `"asks"`} {
		if strings.Contains(string(raw), said) {
			t.Errorf("the shop's graph, which its revision hashes, says %s", said)
		}
	}
	alone := daemonOf(t, shop)
	stopNow(t, shop)
	with := daemonOf(t, startApp(t, []*app.Service{Shop, Audit, synchronous()}))
	if !slices.Equal(alone.components, with.components) || !slices.Equal(alone.loops, with.loops) || alone.goroutines != with.goroutines {
		t.Errorf("commands that are not queued cost the daemon something:\n alone %+v\n with  %+v", alone, with)
	}
}

// benchApp runs services outside dev — the hub off, as in production — for
// a benchmark.
func benchApp(b *testing.B, services ...*app.Service) {
	b.Helper()
	a := app.NewApp("bench", services...).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Logs(io.Discard))
	if err := a.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := a.Stop(context.Background()); err != nil {
			b.Logf("cleanup: %v", err)
		}
	})
}

// A dispatch costs what a call of an endpoint costs: the same pipeline, a
// span on another node.
func BenchmarkAnOperationInProcess(b *testing.B) {
	s := app.NewService("benched", "What an in-process run costs.")
	echo := func(_ context.Context, in LabInput) (LabInput, error) { return in, nil }
	call := s.Endpoint("POST /benched", echo, app.Name("call"))
	dispatch := s.Command("dispatch", echo)
	ask := s.Query("ask", echo)
	keyed := s.Command("keyed", echo).Key(func(in LabInput) string { return in.Key })
	benchApp(b, s)
	in := LabInput{Key: "k"}
	for name, run := range map[string]func(context.Context, LabInput) (LabInput, error){
		"Endpoint.Call": call.Call, "Command.Dispatch": dispatch.Dispatch, "Query.Ask": ask.Ask, "Command.Dispatch keyed": keyed.Dispatch,
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := run(b.Context(), in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
