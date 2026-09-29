package core_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	model "github.com/kitsunium/sdk/framework/model/internal/core"
)

// moduleGraph is a product that mounts the module reviews, which requires
// ratings: their services are qualified, their nodes marked.
func moduleGraph() *model.GraphMessage {
	return &model.GraphMessage{
		App: model.AppMessage{Name: "shop"},
		Nodes: []model.NodeEntity{
			{ID: "shop", Kind: model.KindService, Name: "shop"},
			{ID: "reviews", Kind: model.KindService, Name: "reviews", Module: "reviews"},
			{ID: "reviews.screening", Kind: model.KindService, Name: "reviews.screening", Module: "reviews"},
			{ID: "ratings", Kind: model.KindService, Name: "ratings", Module: "ratings"},
			{
				ID: "reviews/store/reviews", Kind: model.KindStore, Name: "reviews", Service: "reviews", Module: "reviews",
				Source: &model.SourceMessage{File: "reviews.go", Line: 12, GoModule: "example.com/reviews@v1.2.0"},
			},
		},
		Modules: []model.ModuleMessage{
			{
				Name: "reviews", Services: []string{"reviews", "reviews.screening"}, Requires: []string{"ratings"}, Prefix: "/reviews/",
				Source: &model.SourceMessage{File: "module.go", Line: 9, GoModule: "example.com/reviews@v1.2.0"}, Mount: &model.SourceMessage{File: "main.go", Line: 20},
			},
			{Name: "ratings", Services: []string{"ratings"}, RequiredBy: []string{"reviews"}, Prefix: "/ratings/"},
		},
	}
}

func TestQualifiedNames(t *testing.T) {
	for _, c := range []struct{ module, service, want string }{
		{"", "orders", "orders"},
		{"moderation", "intake", "moderation.intake"},
		{"moderation", "moderation", "moderation"},
	} {
		if got := model.QualifiedService(c.module, c.service); got != c.want {
			t.Errorf("QualifiedService(%q, %q) = %q, want %q", c.module, c.service, got, c.want)
		}
	}
	for _, c := range []struct{ prefix, path, want string }{
		{"/moderation/", "/reports", "/moderation/reports"},
		{"/moderation/", "/", "/moderation/"},
		{"/", "/reports", "/reports"},
		{"", "/reports", "/reports"},
	} {
		if got := model.UnderPrefix(c.prefix, c.path); got != c.want {
			t.Errorf("UnderPrefix(%q, %q) = %q, want %q", c.prefix, c.path, got, c.want)
		}
	}
	if got := model.ModulePrefix("moderation"); got != "/moderation/" {
		t.Errorf("the default prefix is %q", got)
	}
}

func TestNormalizeSortsTheModules(t *testing.T) {
	g := moduleGraph()
	g.Modules[1].RequiredBy = []string{"zeta", "alpha"}
	g.Normalize()
	if g.Modules[0].Name != "ratings" || g.Modules[1].Name != "reviews" {
		t.Errorf("modules are not sorted by name: %v, %v", g.Modules[0].Name, g.Modules[1].Name)
	}
	if !slices.Equal(g.Modules[0].RequiredBy, []string{"alpha", "zeta"}) {
		t.Errorf("requiredBy is not sorted: %v", g.Modules[0].RequiredBy)
	}
	if !slices.Equal(g.Modules[1].Services, []string{"reviews", "reviews.screening"}) {
		t.Errorf("a module's services keep the order it lists them: %v", g.Modules[1].Services)
	}
	if g.ModuleOf("reviews") == nil || g.ModuleOf("nothing") != nil {
		t.Error("ModuleOf finds a module by its name, and only then")
	}
	empty := model.GraphMessage{Modules: []model.ModuleMessage{{Name: "empty", Prefix: "/empty/"}}}
	empty.Normalize()
	raw, err := json.Marshal(empty.Modules)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"services":[]`) {
		t.Errorf("a module without services serves %s", raw)
	}
}

// The modules are structure: a different prefix is a different product.
func TestRevisionHashesTheModules(t *testing.T) {
	a, b := moduleGraph(), moduleGraph()
	b.Modules[0].Prefix = "/trust/"
	a.Normalize()
	b.Normalize()
	if a.Revision == b.Revision {
		t.Error("two prefixes of one module share a revision")
	}
}

// A module's files are its Go module's: the source endpoint serves each
// from its own root.
func TestFilesSaysEachFilesRoot(t *testing.T) {
	g := moduleGraph()
	want := []model.FileMessage{
		{File: "main.go"},
		{GoModule: "example.com/reviews@v1.2.0", File: "module.go"},
		{GoModule: "example.com/reviews@v1.2.0", File: "reviews.go"},
	}
	if got := g.Files(); !slices.Equal(got, want) {
		t.Errorf("Files() = %v, want %v", got, want)
	}
}

// The runtime says which modules the app mounts; the analysis only adds
// what it knows of their nodes. A node the analysis finds on a mounted
// module's service, which the runtime lacks, is said, not dropped unsaid.
func TestMergeTakesTheModulesFromTheRuntime(t *testing.T) {
	base := moduleGraph()
	extra := moduleGraph()
	extra.Modules = append(extra.Modules, model.ModuleMessage{Name: "console", Services: []string{"console"}})
	extra.Nodes = append(extra.Nodes,
		model.NodeEntity{
			ID: "reviews/endpoint/Hidden", Kind: model.KindEndpoint, Name: "Hidden", Service: "reviews", Module: "reviews",
			Source: &model.SourceMessage{File: "api/api.go", Line: 7, GoModule: "example.com/reviews@v1.2.0"},
		},
		model.NodeEntity{ID: "shop/endpoint/Unmounted", Kind: model.KindEndpoint, Name: "Unmounted", Service: "shop"},
	)
	g := model.Merge(base, extra)
	if len(g.Modules) != 2 || g.ModuleOf("console") != nil {
		t.Errorf("the merge took modules the runtime does not mount: %+v", g.Modules)
	}
	if g.Node("reviews/endpoint/Hidden") != nil {
		t.Error("the merge added a node the runtime lacks")
	}
	var said []string
	for _, d := range g.Diagnostics {
		said = append(said, d.Message)
	}
	if len(said) != 1 || !strings.Contains(said[0], "reviews/endpoint/Hidden") || !strings.Contains(said[0], "does not link") {
		t.Errorf("the unlinked declaration is said once, and only it: %q", said)
	}
}

func TestMermaidDrawsAModuleAsASubgraph(t *testing.T) {
	g := moduleGraph()
	g.Normalize()
	out := model.Mermaid(g)
	_, afterModule, drawn := strings.Cut(out, `["module reviews"]`)
	if !drawn || !strings.Contains(afterModule, `["reviews.screening"]`) || !strings.Contains(out, `["shop"]`) {
		t.Fatalf("the module is not drawn around its services:\n%s", out)
	}
	if strings.Count(out, "subgraph") != 6 {
		t.Errorf("want two module subgraphs and four service subgraphs:\n%s", out)
	}
}
