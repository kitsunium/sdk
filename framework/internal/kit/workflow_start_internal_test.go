package kit

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
