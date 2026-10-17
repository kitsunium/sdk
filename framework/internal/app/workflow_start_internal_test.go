package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type watched struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// A workflow that fails to start takes its hooks off its store, so a retried
// Start adds them once; a workflow that stops takes back its own, and
// another workflow over the same store keeps its.
func TestAWorkflowTakesBackItsOwnStoreHooks(t *testing.T) {
	needsFileStore(t)
	svc := NewService("watch", "")
	items := svc.Store("items", func(w watched) string { return w.ID })
	svc.Workflow("first", items, func(w *watched) *string { return &w.State }).Initial("new").On("go", "new", "gone")
	svc.Workflow("second", items, func(w *watched) *string { return &w.State }).Initial("new").On("go", "new", "gone")
	dir := t.TempDir()
	broken := filepath.Join(dir, "watch", "first.workflow.json")
	if err := os.MkdirAll(filepath.Dir(broken), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp("watch", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	hooks := func() (int, int) {
		items.mu.RLock()
		defer items.mu.RUnlock()
		return len(items.onWrite), len(items.onDelete)
	}

	if err := app.Start(t.Context()); err == nil {
		app.Stop(context.Background())
		t.Fatal("a workflow with a broken file started")
	}
	if w, d := hooks(); w != 0 || d != 0 {
		t.Fatalf("after a failed start the store keeps %d write and %d delete hooks", w, d)
	}

	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("the retried start: %v", err)
	}
	if w, d := hooks(); w != 2 || d != 2 {
		t.Fatalf("two workflows over the store hold %d write and %d delete hooks, want 2 and 2", w, d)
	}
	first := app.findNode("watch/workflow/first").(*WorkflowService[watched, string])
	if err := first.stop(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	if w, d := hooks(); w != 1 || d != 1 {
		t.Fatalf("after one workflow stopped the store holds %d write and %d delete hooks, want the other's", w, d)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w, d := hooks(); w != 0 || d != 0 {
		t.Fatalf("after the stop the store keeps %d write and %d delete hooks", w, d)
	}
}

// twin is a state type whose values all spell alike.
type twin int

// String is every twin's spelling.
func (twin) String() string { return "same" }

// twinned is the entity of a workflow over twins.
type twinned struct {
	ID    string `json:"id"`
	State twin   `json:"state"`
}

// The engine knows a state by its spelling (ADR 0179 Q3): a workflow that
// declares two states spelled alike is refused at the start, naming the
// spelling, rather than run as one state; distinct spellings start.
func TestAWorkflowRefusesTwoStatesSpelledAlike(t *testing.T) {
	type tc struct {
		name    string
		to      twin
		refused bool
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		svc := NewService("twins", "")
		items := svc.Store("items", func(w twinned) string { return w.ID })
		svc.Workflow("pair", items, func(w *twinned) *twin { return &w.State }).Initial(1).On("go", 1, c.to)
		app := NewApp("twins", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard))
		err := app.Start(context.Background())
		if err == nil {
			must(t, app.Stop(context.Background()))
		}
		var de *DiagnosticsError
		refused := errors.As(err, &de) && strings.Contains(de.Error(), "two states are spelled same")
		if refused != c.refused {
			t.Errorf("refused = %v, want %v: %v", refused, c.refused, err)
		}
	}
	for _, c := range []tc{{name: "two states spelled alike", to: 2, refused: true}, {name: "one state", to: 1, refused: false}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
