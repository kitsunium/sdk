package app_test

import (
	"io"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
)

// Item is what the facade's store keeps.
type Item struct {
	// ID is the item's key.
	ID string `json:"id"`
}

// A declaration made through the facade points at the product's line, not at
// the facade's: kit reads the caller of the function that forwards.
func TestTheFacadeKeepsDeclarationPositions(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	svc := app.NewService("shelf", "Keeps items.") // the line after Caller's
	svc.Store("items", func(i Item) string { return i.ID })
	a := app.NewApp("facade", svc).With(app.InMemory(), app.Logs(io.Discard))
	g := a.Graph()
	n := g.Node("shelf")
	if n == nil || n.Source == nil || filepath.Base(n.Source.File) != filepath.Base(file) || n.Source.Line != line+1 {
		t.Fatalf("the service is at %+v, want %s:%d", n, filepath.Base(file), line+1)
	}
	st := g.Node("shelf/store/items")
	if st == nil || st.Source == nil || st.Source.Line != line+2 {
		t.Fatalf("the store is at %+v, want line %d", st, line+2)
	}
}

// app.Only through the facade: a mount for another environment leaves its
// module out, and a refusal points at the product's line.
func TestTheFacadesOnlyMountsPerEnvironment(t *testing.T) {
	desk := app.NewService("desk", "A module's service.")
	board := app.NewModule("board", "A module.", desk)
	a := app.NewApp("facade").With(app.InMemory(), app.Logs(io.Discard), app.Env("staging"),
		app.Mount(board, app.Only("dev")))
	if a.Graph().ModuleOf("board") != nil {
		t.Error("a mount for dev holds in staging")
	}
	_, file, line, _ := runtime.Caller(0)
	a = app.NewApp("facade").With(app.InMemory(), app.Logs(io.Discard), app.Mount(board, app.Only())) // the line after Caller's
	for _, d := range a.Graph().Diagnostics {
		if d.Source != nil && filepath.Base(d.Source.File) == filepath.Base(file) && d.Source.Line == line+1 {
			return
		}
	}
	t.Errorf("no problem is said at %s:%d: %+v", filepath.Base(file), line+1, a.Graph().Diagnostics)
}
