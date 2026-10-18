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

// daemonOf reads what app keeps going.
func daemonOf(t *testing.T, a *app.App) daemon {
	t.Helper()
	var d daemon
	g := a.Graph()
	for _, c := range g.Runtime.Components {
		d.components = append(d.components, c.Name)
	}
	for _, l := range g.Runtime.Loops {
		d.loops = append(d.loops, l.Name)
	}
	var gs model.Goroutines
	call(t, a, "GET /_kit/api/goroutines", noBody).json(t, &gs)
	for _, grp := range gs.Groups {
		if grp.Loop != "" && grp.Loop != "http" {
			d.goroutines += grp.Count
		}
	}
	slices.Sort(d.components)
	slices.Sort(d.loops)
	return d
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
