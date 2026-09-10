package view_test

import (
	"bytes"
	"html/template"
	"strconv"
	"testing"
	"testing/fstest"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

// benchTemplate is a page shaped like a real one: a layout, a partial, a
// {{range}} over rows and a handful of escaped values.
const benchTemplate string = `<!doctype html><html><head><title>{{.Title}}</title></head>
<body><h1>{{.Title}}</h1><ul>{{range .Rows}}{{template "row.html" .}}{{end}}</ul>
<a href="{{.Next}}">next</a></body></html>`

// benchRow is the partial the page ranges over.
const benchRow string = `<li title="{{.Name}}"><span>{{.Name}}</span> <em>{{.Value}}</em></li>`

// benchRowCount is how many rows every render emits. Twenty is a list, not a
// report: it keeps the measurement about the engine rather than about memcpy.
const benchRowCount int = 20

// largeTreeFiles is how many templates a real application ships. A page, a
// layout, and a partial per component adds up fast; forty is a small site.
const largeTreeFiles int = 40

// inertTemplate emits a constant. Rendering it costs the same whatever the
// model is, so the delta between BenchmarkScanGatePruned and
// BenchmarkScanFullWalk is the trust-type scan and nothing else.
const inertTemplate string = `<p>x</p>`

// benchModel is the typed render model. It carries no interface-typed field,
// which is what lets the trust-type scan prune it at the gate.
type benchModel struct {
	// Title lands between tags and inside a <title>.
	Title string
	// Rows is the collection the page ranges over.
	Rows []benchRowModel
	// Next lands in a URL context.
	Next string
}

// benchRowModel is one row of [benchModel].
type benchRowModel struct {
	// Name lands in an attribute and between tags.
	Name string
	// Value lands between tags.
	Value int
}

// benchFS is the two-file tree every benchmark parses.
func benchFS() fstest.MapFS {
	return fstest.MapFS{
		"page.html": &fstest.MapFile{Data: []byte(benchTemplate)},
		"row.html":  &fstest.MapFile{Data: []byte(benchRow)},
	}
}

// benchData builds the typed model.
func benchData() benchModel {
	rows := make([]benchRowModel, benchRowCount)
	for index := range rows {
		rows[index] = benchRowModel{Name: "row <name> & co", Value: index}
	}
	return benchModel{Title: "Dashboard <b>", Rows: rows, Next: "/next?page=2&size=20"}
}

// benchDynamicData is the SAME page's model built out of any, which is what a
// handler passing map[string]any produces. The gate cannot prune it, so the
// trust-type scan walks every value.
func benchDynamicData() map[string]any {
	rows := make([]any, benchRowCount)
	for index := range rows {
		rows[index] = map[string]any{"Name": "row <name> & co", "Value": index}
	}
	return map[string]any{"Title": "Dashboard <b>", "Rows": rows, "Next": "/next?page=2&size=20"}
}

// BenchmarkRenderParsedOnce is the shape every consumer should have: the
// renderer is built at start-up and reused for the life of the process.
func BenchmarkRenderParsedOnce(b *testing.B) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: benchFS()})
	if err != nil {
		b.Fatalf("NewHTML: %v", err)
	}
	data := benchData()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		document, renderErr := renderer.Render(ctx, "page.html", data)
		if renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
		if len(document) == 0 {
			b.Fatal("empty document")
		}
	}
}

// BenchmarkRenderReparsedEveryTime is the anti-pattern, measured rather than
// asserted: a renderer constructed per request re-reads the tree, re-parses
// every file and re-runs the escaping analysis before it renders anything.
//
// The ratio against [BenchmarkRenderParsedOnce] is the number the parse-once
// rule is written from.
func BenchmarkRenderReparsedEveryTime(b *testing.B) {
	files := benchFS()
	data := benchData()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		renderer, err := svcview.NewHTML(coreview.Config{FS: files})
		if err != nil {
			b.Fatalf("NewHTML: %v", err)
		}
		document, renderErr := renderer.Render(ctx, "page.html", data)
		if renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
		if len(document) == 0 {
			b.Fatal("empty document")
		}
	}
}

// BenchmarkConstruct isolates the construction half of the anti-pattern: the
// walk, the parse and the escaping probe, with no render at all.
func BenchmarkConstruct(b *testing.B) {
	files := benchFS()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svcview.NewHTML(coreview.Config{FS: files}); err != nil {
			b.Fatalf("NewHTML: %v", err)
		}
	}
}

// BenchmarkRenderTypedModel and BenchmarkRenderDynamicModel render the SAME
// page with the same content, differing only in whether the model's static
// type can hold a trust type.
//
// The delta is the price of the trust-type scan when the gate cannot prune it.
// A typed view-model pays one map lookup; a map[string]any pays a walk of
// every value on every render.
func BenchmarkRenderTypedModel(b *testing.B) {
	benchmarkRender(b, benchData())
}

// BenchmarkRenderDynamicModel is BenchmarkRenderTypedModel's counterpart over
// a map[string]any model.
func BenchmarkRenderDynamicModel(b *testing.B) {
	benchmarkRender(b, benchDynamicData())
}

// benchmarkRender renders the bench page against data.
func benchmarkRender(b *testing.B, data any) {
	b.Helper()
	renderer, err := svcview.NewHTML(coreview.Config{FS: benchFS()})
	if err != nil {
		b.Fatalf("NewHTML: %v", err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		document, renderErr := renderer.Render(ctx, "page.html", data)
		if renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
		if len(document) == 0 {
			b.Fatal("empty document")
		}
	}
}

// BenchmarkRenderParallel measures the pooled scratch under contention, which
// is the only shape a request path ever has.
func BenchmarkRenderParallel(b *testing.B) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: benchFS()})
	if err != nil {
		b.Fatalf("NewHTML: %v", err)
	}
	data := benchData()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			document, renderErr := renderer.Render(ctx, "page.html", data)
			if renderErr != nil {
				b.Fatalf("Render: %v", renderErr)
			}
			if len(document) == 0 {
				b.Fatal("empty document")
			}
		}
	})
}

// largeFS is benchFS plus enough filler templates to make the tree the size a
// deployed application actually has.
//
// The tree size is the whole point of this pair: reparsing costs O(tree) and
// rendering costs O(page), so the two-file measurement understates the
// anti-pattern by exactly the factor a real tree is bigger.
func largeFS() fstest.MapFS {
	files := benchFS()
	for index := range largeTreeFiles {
		name := "filler/" + strconv.Itoa(index) + ".html"
		files[name] = &fstest.MapFile{Data: []byte(`<section id="s{{.}}"><p>{{.}}</p></section>`)}
	}
	return files
}

// BenchmarkRenderParsedOnceLargeTree renders one page out of a forty-template
// tree, with the renderer built once.
func BenchmarkRenderParsedOnceLargeTree(b *testing.B) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: largeFS()})
	if err != nil {
		b.Fatalf("NewHTML: %v", err)
	}
	data := benchData()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		document, renderErr := renderer.Render(ctx, "page.html", data)
		if renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
		if len(document) == 0 {
			b.Fatal("empty document")
		}
	}
}

// BenchmarkRenderReparsedEveryTimeLargeTree is the same page out of the same
// tree, reconstructed per render. This is the pair the parse-once rule quotes.
func BenchmarkRenderReparsedEveryTimeLargeTree(b *testing.B) {
	files := largeFS()
	data := benchData()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		renderer, err := svcview.NewHTML(coreview.Config{FS: files})
		if err != nil {
			b.Fatalf("NewHTML: %v", err)
		}
		document, renderErr := renderer.Render(ctx, "page.html", data)
		if renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
		if len(document) == 0 {
			b.Fatal("empty document")
		}
	}
}

// BenchmarkStdlibBaseline renders the identical page with raw html/template
// into a bytes.Buffer, with no port, no scan, no ceiling and no copy-out.
//
// It is the honest denominator for every other number here: what the SDK's
// contract costs over the engine it is built on.
func BenchmarkStdlibBaseline(b *testing.B) {
	set := template.New("")
	if _, err := set.New("page.html").Parse(benchTemplate); err != nil {
		b.Fatalf("parse page: %v", err)
	}
	if _, err := set.New("row.html").Parse(benchRow); err != nil {
		b.Fatalf("parse row: %v", err)
	}
	page := set.Lookup("page.html")
	data := benchData()
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf.Reset()
		if err := page.Execute(&buf, data); err != nil {
			b.Fatalf("Execute: %v", err)
		}
	}
}

// BenchmarkScanGatePruned renders the inert page against a TYPED model. The
// gate answers "this type cannot hold a trust type" from one cached map
// lookup, and no value is touched.
func BenchmarkScanGatePruned(b *testing.B) {
	benchmarkScan(b, benchData())
}

// BenchmarkScanFullWalk renders the same inert page against the equivalent
// map[string]any model. Every value is dynamic, so the gate cannot prune and
// the scan walks the whole graph on every render.
func BenchmarkScanFullWalk(b *testing.B) {
	benchmarkScan(b, benchDynamicData())
}

// benchmarkScan renders the inert page against data.
func benchmarkScan(b *testing.B, data any) {
	b.Helper()
	renderer, err := svcview.NewHTML(coreview.Config{
		FS: fstest.MapFS{"inert.html": &fstest.MapFile{Data: []byte(inertTemplate)}},
	})
	if err != nil {
		b.Fatalf("NewHTML: %v", err)
	}
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, renderErr := renderer.Render(ctx, "inert.html", data); renderErr != nil {
			b.Fatalf("Render: %v", renderErr)
		}
	}
}
