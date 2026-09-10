package view_test

import (
	"errors"
	"html/template"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/pkg/v1/view"
)

// tree is the one-file template set most of these tests render.
func tree(body string) fstest.MapFS {
	return fstest.MapFS{"page.html": &fstest.MapFile{Data: []byte(body)}}
}

// TestTheFacadeRendersEndToEnd is the quickstart in the package doc, executed.
func TestTheFacadeRendersEndToEnd(t *testing.T) {
	renderer, err := view.New(view.Config{FS: tree(`<p>{{.}}</p>`), Ext: []string{".html"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", "hello")
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if string(document) != "<p>hello</p>" {
		t.Fatalf("document = %q", document)
	}
	if renderer.ContentType() != view.ContentTypeHTML {
		t.Fatalf("ContentType = %q", renderer.ContentType())
	}
}

// TestEscapingSurvivesTheFacade. A facade that quietly stopped escaping would
// be the worst possible defect in this package, so the property is asserted
// here too rather than only one layer down.
func TestEscapingSurvivesTheFacade(t *testing.T) {
	renderer, err := view.New(view.Config{FS: tree(`<p>{{.}}</p>`)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", `<script>alert(1)</script>`)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if strings.Contains(string(document), "<script>alert(1)") {
		t.Fatalf("the payload was not escaped: %q", document)
	}
}

// TestTrustHTMLBypassesEscapingThroughTheFacade — and nothing else does.
func TestTrustHTMLBypassesEscapingThroughTheFacade(t *testing.T) {
	renderer, err := view.New(view.Config{FS: tree(`<div>{{.}}</div>`)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", view.TrustHTML("<b>ok</b>"))
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if !strings.Contains(string(document), "<b>ok</b>") {
		t.Fatalf("TrustHTML was escaped: %q", document)
	}
}

// TestARefusedTrustTypeIsStillRefusedThroughTheFacade.
func TestARefusedTrustTypeIsStillRefusedThroughTheFacade(t *testing.T) {
	renderer, err := view.New(view.Config{FS: tree(`<p>{{.}}</p>`)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", template.URL("javascript:alert(1)"))
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, view.UnsafeValue) {
		t.Fatalf("err = %v, want UnsafeValue", renderErr)
	}
}

// TestTheAliasesAreTheSameTypes. A facade that re-declared them would make a
// consumer's Renderer incompatible with the port every other package speaks.
func TestTheAliasesAreTheSameTypes(t *testing.T) {
	if reflect.TypeFor[view.TrustedHTML]() != reflect.TypeFor[template.HTML]() {
		t.Fatal("view.TrustedHTML is not html/template.HTML; escaping will not be bypassed")
	}
	if reflect.TypeFor[view.Renderer]().NumMethod() != 2 {
		t.Fatalf("Renderer has %d methods, want 2 (ADR 0039 freeze)",
			reflect.TypeFor[view.Renderer]().NumMethod())
	}
}

// TestTheHTMLEngineIsRegisteredByImportingThisPackage: the facade's own import
// of the engine is what wires it, so a config-driven Open works with no
// further blank import.
func TestTheHTMLEngineIsRegisteredByImportingThisPackage(t *testing.T) {
	renderer, err := view.Open(view.HTML, view.Config{FS: tree("REGISTERED")})
	if err != nil {
		t.Fatalf("Open(HTML): %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", nil)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if string(document) != "REGISTERED" {
		t.Fatalf("document = %q", document)
	}
	found := false
	for _, engine := range view.Available() {
		if engine == view.HTML {
			found = true
		}
	}
	if !found {
		t.Fatalf("Available() = %v, missing %q", view.Available(), view.HTML)
	}
}

// TestOpenRefusesAnUnknownEngineRatherThanDefaulting: a typo in a config file
// must not silently choose how every value in the program is escaped.
func TestOpenRefusesAnUnknownEngineRatherThanDefaulting(t *testing.T) {
	renderer, err := view.Open("mustache", view.Config{FS: tree("x")})
	if renderer != nil {
		t.Fatal("Open returned a Renderer for an unregistered engine")
	}
	if !errors.Is(err, view.EngineUnknown) {
		t.Fatalf("err = %v, want EngineUnknown", err)
	}
}

// TestABrokenTreeIsRefusedAndTheRendererIsGenuinelyNil.
//
// A typed nil boxed into the interface would satisfy `renderer != nil` and
// panic on the first request — in a domain whose whole contract is that a
// broken tree stops the deploy.
func TestABrokenTreeIsRefusedAndTheRendererIsGenuinelyNil(t *testing.T) {
	for name, cfg := range map[string]view.Config{
		"a nil FS":                         {},
		"a file that will not parse":       {FS: tree("{{if}}")},
		"an unresolvable escaping context": {FS: tree(`<a href="{{.}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			renderer, err := view.New(cfg)
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if renderer != nil {
				t.Fatal("a non-nil Renderer was returned alongside an error")
			}
		})
	}
}

// TestTheCeilingIsReachableThroughTheFacade, and there is no "unlimited".
func TestTheCeilingIsReachableThroughTheFacade(t *testing.T) {
	renderer, err := view.New(view.Config{FS: tree(`{{range .}}0123456789{{end}}`), MaxBytes: 32})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "page.html", make([]int, 100))
	if document != nil {
		t.Fatalf("document = %d bytes, want nil", len(document))
	}
	if !errors.Is(renderErr, view.RenderTooLarge) {
		t.Fatalf("err = %v, want RenderTooLarge", renderErr)
	}
	if view.DefaultMaxBytes <= 0 {
		t.Fatal("DefaultMaxBytes must be positive; there is no unlimited spelling")
	}
}

// TestRegisterRefusesABrokenRegistrationThroughTheFacade keeps the boot-time
// panic reachable from the public surface a third-party engine would use.
func TestRegisterRefusesABrokenRegistrationThroughTheFacade(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Register(nil) did not panic")
		}
	}()
	view.Register(nil)
}
