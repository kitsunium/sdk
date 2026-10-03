package kit

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// warnDoc is what the warned app keeps.
type warnDoc struct {
	ID string `json:"id"`
}

// In dev, a command whose code writes stores of two databases — the data
// directory counting as one — is warned of, and so is a kit.Transact
// function whose steps do; a command without a transaction, and one that
// writes one database, are not; outside dev, kit says nothing.
func TestATransactionOverTwoDatabasesIsWarnedOf(t *testing.T) {
	svc := NewService("twice", "")
	main := svc.Store("main", func(d warnDoc) string { return d.ID })
	svc.Store("files", func(d warnDoc) string { return d.ID })
	svc.Store("cache", func(d warnDoc) string { return d.ID }, InMemory())
	noop := func(context.Context, warnDoc) (EmptyValue, error) { return EmptyValue{}, nil }
	svc.Command("across", noop)
	svc.Command("loose", noop, NoTransaction())
	svc.Command("one", noop)
	db := NewFakeDB(sql.DialectPostgres)
	app := NewApp("warn", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard),
		Database("main", db.Engine(), Keeps(main)))
	must(t, app.Start(t.Context()))
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	writes := func(from, to string) model.Edge {
		return model.Edge{From: "twice/command/" + from, To: "twice/store/" + to, Kind: model.EdgeWrites}
	}
	f := model.CodeFunc{
		Func: "example.com/twice.Move", Source: &model.Source{File: "twice.go", Line: 10},
		Effects: []model.CodeEffect{{Kind: model.EdgeWrites, Target: "twice/store/main"}, {Kind: model.EdgeWrites, Target: "twice/store/files"}},
		Steps:   []model.CodeStep{{Line: 12, Effect: 1, Block: 1}, {Line: 13, Effect: 2, Block: 2}},
		Blocks:  []model.CodeBlock{{Kind: model.BlockTransaction, Label: "kit.Transact", Line: 11}, {Kind: model.BlockIf, Line: 13, Parent: 1}},
	}
	static := &model.Graph{
		Edges: []model.Edge{
			writes("across", "main"), writes("across", "files"), writes("loose", "main"), writes("loose", "files"),
			writes("one", "files"), writes("one", "cache"),
		},
		Nodes: []model.Node{{ID: "twice/endpoint/Move", Code: &model.CodeInfo{Funcs: []model.CodeFunc{f}}}},
	}
	app.mu.Lock()
	app.static = static
	app.mu.Unlock()
	var warned []string
	for _, d := range app.Graph().Diagnostics {
		if d.Severity != "warning" || !strings.Contains(d.Message, "a transaction writes one database") {
			continue
		}
		if d.Source == nil || d.Texts["fr"] == "" {
			t.Errorf("a warning with no position or no French: %+v", d)
		}
		warned = append(warned, d.Node)
	}
	slices.Sort(warned)
	if want := []string{"twice/command/across", "twice/endpoint/Move"}; !slices.Equal(warned, want) {
		t.Errorf("warned of %v, want %v", warned, want)
	}
	prod := &App{cfg: config{env: EnvProduction}}
	if got := prod.transactionWarnings(app.Graph(), static); got != nil {
		t.Errorf("outside dev: %v", got)
	}
}
