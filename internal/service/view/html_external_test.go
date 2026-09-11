package view_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

// mustRender builds a renderer over files and renders name, failing the test
// on any error. It is the shape most escaping assertions need.
func mustRender(tb testing.TB, files fstest.MapFS, name string, data any) string {
	tb.Helper()
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		tb.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(tb.Context(), name, data)
	if renderErr != nil {
		tb.Fatalf("Render(%q): %v", name, renderErr)
	}
	return string(document)
}

// page is a one-file MapFS helper.
func page(name, body string) fstest.MapFS {
	return fstest.MapFS{name: &fstest.MapFile{Data: []byte(body)}}
}

// TestANilFSIsRefusedAtConstruction is ADR 0031's refuse half: the one Config
// field with no defensible default.
func TestANilFSIsRefusedAtConstruction(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{})
	if renderer != nil {
		t.Fatal("NewHTML returned a Renderer for a nil FS")
	}
	if !errors.Is(err, coreview.ViewMisconfigured) {
		t.Fatalf("err = %v, want ViewMisconfigured", err)
	}
}

// TestAnEmptyTreeBuildsARendererThatRefusesEveryName is ADR 0031's other half.
//
// A Renderer over an empty tree is legitimate and refuses everything BY NAME.
// The alternative — an inert renderer returning empty pages — is the failure
// mode the rule exists to prevent.
func TestAnEmptyTreeBuildsARendererThatRefusesEveryName(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: fstest.MapFS{}})
	if err != nil {
		t.Fatalf("NewHTML over an empty tree: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "anything.html", nil)
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, coreview.TemplateNotFound) {
		t.Fatalf("err = %v, want TemplateNotFound", renderErr)
	}
}

// TestTheEmptyNameIsRefusedAndNeverResolvesTheAnchor closes the second of the
// two independent mechanisms guarding the set's unnamed anchor template.
func TestTheEmptyNameIsRefusedAndNeverResolvesTheAnchor(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("a.html", "hi")})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "", nil)
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, coreview.TemplateNotFound) {
		t.Fatalf("err = %v, want TemplateNotFound", renderErr)
	}
}

// TestTemplatesAreNamedByTheirFullSlashPath is the load-bearing naming claim.
//
// html/template's own ParseFS names each template by filepath.Base, so a tree
// holding admin/page.html and user/page.html ends up with ONE template called
// "page.html": the second parse silently wins and every request for the admin
// page renders the user page. This test fails if the SDK ever inherits that.
func TestTemplatesAreNamedByTheirFullSlashPath(t *testing.T) {
	files := fstest.MapFS{
		"admin/page.html": &fstest.MapFile{Data: []byte("ADMIN")},
		"user/page.html":  &fstest.MapFile{Data: []byte("USER")},
	}
	if got := mustRender(t, files, "admin/page.html", nil); got != "ADMIN" {
		t.Fatalf("admin/page.html rendered %q", got)
	}
	if got := mustRender(t, files, "user/page.html", nil); got != "USER" {
		t.Fatalf("user/page.html rendered %q", got)
	}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	if _, baseErr := renderer.Render(t.Context(), "page.html", nil); !errors.Is(baseErr, coreview.TemplateNotFound) {
		t.Fatalf("the basename resolved; full-path naming has been lost (err = %v)", baseErr)
	}
}

// TestPartialsResolveAcrossFilesByFullPath: set.New(entry) makes every file an
// ASSOCIATED template, so {{template "partial/row.html"}} resolves.
func TestPartialsResolveAcrossFilesByFullPath(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte(`<ul>{{template "partial/row.html" .}}</ul>`)},
		"partial/row.html": &fstest.MapFile{Data: []byte(`<li>{{.}}</li>`)},
	}
	if got := mustRender(t, files, "index.html", "hi"); got != "<ul><li>hi</li></ul>" {
		t.Fatalf("rendered %q", got)
	}
}

// TestExtRestrictsWhichFilesAreParsed, and an empty Ext takes the tree as
// given — the caller already chose it.
func TestExtRestrictsWhichFilesAreParsed(t *testing.T) {
	files := fstest.MapFS{
		"page.html": &fstest.MapFile{Data: []byte("PAGE")},
		"logo.svg":  &fstest.MapFile{Data: []byte("<svg>{{")},
	}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files, Ext: []string{".html"}})
	if err != nil {
		t.Fatalf("NewHTML with Ext: %v", err)
	}
	if _, svgErr := renderer.Render(t.Context(), "logo.svg", nil); !errors.Is(svgErr, coreview.TemplateNotFound) {
		t.Fatalf("logo.svg was parsed despite Ext (err = %v)", svgErr)
	}
	if _, noExt := svcview.NewHTML(coreview.Config{FS: files}); noExt == nil {
		t.Fatal("without Ext the unparseable .svg should have been parsed and refused")
	}
}

// TestAFileThatDoesNotParseIsRefusedAtConstruction: a permanent fault belongs
// in the process's first second, not in the first request that reaches it.
func TestAFileThatDoesNotParseIsRefusedAtConstruction(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("broken.html", "{{if}}")})
	if renderer != nil {
		t.Fatal("NewHTML returned a Renderer over an unparseable tree")
	}
	if !errors.Is(err, svcview.TemplateParseFailed) {
		t.Fatalf("err = %v, want TemplateParseFailed", err)
	}
}

// TestTheEscapingProbeMovesLazyFailuresToBootTime is the construction-time
// claim that matters most.
//
// html/template resolves a template's escaping context at its FIRST execution.
// Left alone, all three of these are a 500 on whichever request first reaches
// that page — and the third is the everyday one: a partial gets renamed, every
// test that exercises another page passes, and the failure ships.
func TestTheEscapingProbeMovesLazyFailuresToBootTime(t *testing.T) {
	cases := map[string]string{
		"ErrEndContext ends inside an unterminated attribute": `<a href="{{.}}`,
		"ErrBranchEnd branches end in different contexts":     `<script>{{if .}}var x = "{{end}}</script>`,
		"ErrNoSuchTemplate names a partial that is not there": `{{template "renamed.html"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", body)})
			if renderer != nil {
				t.Fatal("NewHTML accepted a template whose escaping cannot be resolved")
			}
			if !errors.Is(err, svcview.TemplateParseFailed) {
				t.Fatalf("err = %v, want TemplateParseFailed", err)
			}
		})
	}
}

// TestATemplateThatReadsANilModelIsNotRefusedAtConstruction is the probe's
// other half: only an ESCAPING failure is fatal.
//
// Executing with a nil model can legitimately fail — a field chain on nothing
// — and refusing on that would make every template that reads its model
// unusable.
func TestATemplateThatReadsANilModelIsNotRefusedAtConstruction(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `<p>{{.User.Name}}</p>`)})
	if err != nil {
		t.Fatalf("NewHTML refused a template that merely reads its model: %v", err)
	}
	if renderer == nil {
		t.Fatal("NewHTML returned no Renderer and no error")
	}
}

// TestASelfRecursiveTemplateIsRefusedAtConstruction pins that a template which
// reaches itself through {{template}} calls with no condition on the way is a
// defect of the tree, refused with the tree's other defects. The probe used to
// execute it with a nil model, let text/template stop it 100 000 calls down,
// and discard that error like any nil-model failure — so the renderer came up
// and every render recursed that deep and failed. A guarded recursion, which is
// how a nested model is legitimately rendered, must still build. Seen failing
// without the check: "NewHTML accepted a template that never returns" for both
// the direct and the indirect cycle.
func TestASelfRecursiveTemplateIsRefusedAtConstruction(t *testing.T) {
	for name, body := range map[string]string{
		"a template that calls itself": `{{define "loop"}}{{template "loop" .}}{{end}}{{template "loop" .}}`,
		"two templates calling each other": `{{define "a"}}{{template "b" .}}{{end}}` +
			`{{define "b"}}<p>{{template "a" .}}</p>{{end}}{{template "a" .}}`,
	} {
		t.Run(name, func(t *testing.T) {
			renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", body)})
			if renderer != nil {
				t.Fatal("NewHTML accepted a template that never returns")
			}
			if !errors.Is(err, svcview.TemplateParseFailed) {
				t.Fatalf("err = %v, want TemplateParseFailed", err)
			}
		})
	}
	guarded := `{{define "node"}}{{if .Next}}{{template "node" .Next}}{{end}}{{end}}{{template "node" .}}`
	if _, gerr := svcview.NewHTML(coreview.Config{FS: page("tree.html", guarded)}); gerr != nil {
		t.Fatalf("NewHTML refused a guarded recursion: %v", gerr)
	}
}

// TestASourceFailureIsNotAParseFailure: the two have different operators and
// different fixes, so they carry different codes.
func TestASourceFailureIsNotAParseFailure(t *testing.T) {
	_, err := svcview.NewHTML(coreview.Config{FS: failingFS{}})
	if !errors.Is(err, svcview.TemplateSourceFailed) {
		t.Fatalf("err = %v, want TemplateSourceFailed", err)
	}
	if errors.Is(err, svcview.TemplateParseFailed) {
		t.Fatal("a source failure was reported as a parse failure")
	}
}

// TestTheFactoryIsRegisteredAndReachableThroughOpen wires the config-driven
// path end to end.
func TestTheFactoryIsRegisteredAndReachableThroughOpen(t *testing.T) {
	renderer, err := coreview.Open(coreview.HTML, coreview.Config{FS: page("p.html", "REGISTERED")})
	if err != nil {
		t.Fatalf("Open(HTML): %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "p.html", nil)
	if renderErr != nil {
		t.Fatalf("Render: %v", renderErr)
	}
	if string(document) != "REGISTERED" {
		t.Fatalf("document = %q", document)
	}
	if svcview.HTML.Engine() != coreview.HTML {
		t.Fatalf("the factory claims %q, not %q", svcview.HTML.Engine(), coreview.HTML)
	}
}

// TestContentTypeIsTheHTMLOne pins the promise the engine makes about the
// escaping it performs.
func TestContentTypeIsTheHTMLOne(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: fstest.MapFS{}})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	if renderer.ContentType() != coreview.ContentTypeHTML {
		t.Fatalf("ContentType = %q", renderer.ContentType())
	}
}

// TestTemplateNotFoundNamesTheCountInAField so "the renderer is empty" and
// "that one name is wrong" are one log line apart.
func TestTemplateNotFoundNamesTheCountInAField(t *testing.T) {
	files := fstest.MapFS{
		"a.html": &fstest.MapFile{Data: []byte("A")},
		"b.html": &fstest.MapFile{Data: []byte("B")},
	}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	_, renderErr := renderer.Render(t.Context(), "c.html", nil)
	fields := fieldMap(errs.FieldsOf(renderErr))
	if fields["templates"] != "2" {
		t.Fatalf("templates field = %q, want 2", fields["templates"])
	}
	if fields["template"] != "c.html" {
		t.Fatalf("template field = %q", fields["template"])
	}
	if strings.Contains(renderErr.Error(), "c.html") {
		t.Fatalf("the requested name leaked into Error(): %q", renderErr.Error())
	}
}

// fieldMap flattens errs fields into key → value.
func fieldMap(fields []errs.FieldValue) map[string]string {
	out := make(map[string]string, len(fields))
	for _, field := range fields {
		out[field.Key()] = field.StringValue()
	}
	return out
}
