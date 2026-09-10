package view_test

import (
	"errors"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

// TestEscapingIsContextual is the reason this domain exists.
//
// The SAME string is escaped differently depending on where it lands. A
// uniform escaper — or none — passes the first of these cases and ships the
// rest, which is why "it escapes" is not the property being claimed.
func TestEscapingIsContextual(t *testing.T) {
	cases := []struct {
		// name says which HTML context the value lands in.
		name string
		// body is the template.
		body string
		// payload is the value handed to it.
		payload string
		// want is the escaping that context requires.
		want string
		// absent is the break-out this escaping prevents.
		absent string
	}{
		{
			"between tags", `<p>{{.}}</p>`, `<script>alert(1)</script>`,
			"&lt;script&gt;", "<script>alert(1)",
		},
		{
			"in an attribute", `<p title="{{.}}">x</p>`, `" onmouseover="alert(1)`,
			"&#34;", `" onmouseover="`,
		},
		{
			"in a script block", `<script>var v = {{.}};</script>`, `</script><b>`,
			`\u003c`, "</script><b>",
		},
		{
			"in a URL", `<a href="{{.}}">x</a>`, "https://x/?a=1&b=2",
			"&amp;b=2", "?a=1&b=2",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			document := mustRender(t, page("p.html", testCase.body), "p.html", testCase.payload)
			if !strings.Contains(document, testCase.want) {
				t.Fatalf("%q is not escaped as %q: %q", testCase.payload, testCase.want, document)
			}
			if strings.Contains(document, testCase.absent) {
				t.Fatalf("%q broke out into %q", testCase.absent, document)
			}
		})
	}
}

// TestOneValueIsEscapedTwoWaysInTwoContexts is the contextual property in a
// single assertion: the identical byte becomes &lt; between tags and \u003c
// inside a script block. A uniform escaper cannot produce both.
func TestOneValueIsEscapedTwoWaysInTwoContexts(t *testing.T) {
	files := fstest.MapFS{
		"markup.html": &fstest.MapFile{Data: []byte(`<p>{{.}}</p>`)},
		"script.html": &fstest.MapFile{Data: []byte(`<script>var v = {{.}};</script>`)},
	}
	const payload string = "<x>"
	markup := mustRender(t, files, "markup.html", payload)
	script := mustRender(t, files, "script.html", payload)
	if !strings.Contains(markup, "&lt;x&gt;") {
		t.Fatalf("markup context = %q", markup)
	}
	if !strings.Contains(script, `\u003cx\u003e`) {
		t.Fatalf("script context = %q", script)
	}
	if markup == script {
		t.Fatal("the two contexts produced the same escaping; this is not contextual")
	}
}

// TestAJavascriptURLIsNeutralisedWhenItIsAPlainString records the behaviour
// the six refused trust types would each disable.
func TestAJavascriptURLIsNeutralisedWhenItIsAPlainString(t *testing.T) {
	document := mustRender(t, page("p.html", `<a href="{{.}}">x</a>`), "p.html", "javascript:alert(1)")
	if !strings.Contains(document, "#ZgotmplZ") {
		t.Fatalf("a javascript: URL was not neutralised: %q", document)
	}
}

// TestTrustHTMLIsTheOneBypassAndItWorks. If this fails, TrustHTML is a trust
// type that does not confer trust — worse than none, because callers stop
// looking for the reason their markup disappeared.
func TestTrustHTMLIsTheOneBypassAndItWorks(t *testing.T) {
	trusted := mustRender(t, page("p.html", `<div>{{.}}</div>`), "p.html", coreview.TrustHTML("<b>bold</b>"))
	if !strings.Contains(trusted, "<b>bold</b>") {
		t.Fatalf("TrustHTML was escaped: %q", trusted)
	}
	plain := mustRender(t, page("p.html", `<div>{{.}}</div>`), "p.html", "<b>bold</b>")
	if strings.Contains(plain, "<b>bold</b>") {
		t.Fatalf("an untrusted string was NOT escaped: %q", plain)
	}
}

// TestEachRefusedTrustTypeIsRefusedByName walks all six.
//
// Each disables contextual escaping for a context where the same string could
// not have done any harm, and none of the six has an SDK spelling.
func TestEachRefusedTrustTypeIsRefusedByName(t *testing.T) {
	cases := []struct {
		// typeName is the name the error must report.
		typeName string
		// value is a render model that is itself the refused trust type.
		value any
	}{
		{"html/template.CSS", template.CSS("color:red")},
		{"html/template.HTMLAttr", template.HTMLAttr(`onclick="x()"`)},
		{"html/template.JS", template.JS("alert(1)")},
		{"html/template.JSStr", template.JSStr("x")},
		{"html/template.Srcset", template.Srcset("a.png 1x")},
		{"html/template.URL", template.URL("javascript:alert(1)")},
	}
	for _, testCase := range cases {
		t.Run(testCase.typeName, func(t *testing.T) {
			renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `<p>{{.}}</p>`)})
			if err != nil {
				t.Fatalf("NewHTML: %v", err)
			}
			document, renderErr := renderer.Render(t.Context(), "p.html", testCase.value)
			if document != nil {
				t.Fatalf("document = %q, want nil", document)
			}
			if !errors.Is(renderErr, coreview.UnsafeValue) {
				t.Fatalf("err = %v, want UnsafeValue", renderErr)
			}
			if got := fieldMap(errs.FieldsOf(renderErr))["type"]; got != testCase.typeName {
				t.Fatalf("type field = %q, want %q", got, testCase.typeName)
			}
		})
	}
}

// TestTheAdmittedTrustTypeIsNotRefused: template.HTML is the seventh, and the
// scan must not swallow the one bypass the domain deliberately offers.
func TestTheAdmittedTrustTypeIsNotRefused(t *testing.T) {
	document := mustRender(t, page("p.html", `<p>{{.}}</p>`), "p.html", template.HTML("<i>ok</i>"))
	if !strings.Contains(document, "<i>ok</i>") {
		t.Fatalf("template.HTML did not survive: %q", document)
	}
}

// nested is a model with a refused type buried behind a struct, a slice, a
// pointer and an interface — every descent the scan has to make.
type nested struct {
	// Title is an ordinary field the scan must walk past.
	Title string
	// Items holds the branch the violation is buried in.
	Items []*item
}

// item is one element of [nested.Items].
type item struct {
	// Body is an any so the violation's type is only known at render time.
	Body any
}

// TestTheViolationPathNamesWhereTheValueIs. "An unsafe value is somewhere in
// your model" is not something a developer can act on at three in the morning.
func TestTheViolationPathNamesWhereTheValueIs(t *testing.T) {
	cases := []struct {
		// name describes the shape being scanned.
		name string
		// data is the render model.
		data any
		// path is the location the error must report.
		path string
	}{
		{"a struct field", struct{ Style template.CSS }{"a{}"}, "data.Style"},
		{"a slice element", []template.URL{"a", "b"}, "data[0]"},
		{"a map value", map[string]template.JS{"boot": "x()"}, "data[boot]"},
		{"a map key", map[template.URL]string{"u": "x"}, "data[u](key)"},
		{
			"through struct, slice, pointer and interface",
			nested{Title: "t", Items: []*item{{Body: "safe"}, {Body: template.JS("alert(1)")}}},
			"data.Items[1].Body",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `<p>x</p>`)})
			if err != nil {
				t.Fatalf("NewHTML: %v", err)
			}
			_, renderErr := renderer.Render(t.Context(), "p.html", testCase.data)
			if !errors.Is(renderErr, coreview.UnsafeValue) {
				t.Fatalf("err = %v, want UnsafeValue", renderErr)
			}
			if got := fieldMap(errs.FieldsOf(renderErr))["path"]; got != testCase.path {
				t.Fatalf("path = %q, want %q", got, testCase.path)
			}
		})
	}
}

// TestTheScanIsRefusedBeforeTheTemplateRuns: a violation must have no partial
// page to reason about.
//
// The template here writes a marker before it would reach the value. If the
// scan ran after execution, the marker would exist somewhere; because it runs
// first, the template is never executed at all.
func TestTheScanIsRefusedBeforeTheTemplateRuns(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `PREFIX-MARKER{{.Style}}`)})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(), "p.html", struct{ Style template.CSS }{"a{}"})
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	if !errors.Is(renderErr, coreview.UnsafeValue) {
		t.Fatalf("err = %v, want UnsafeValue", renderErr)
	}
	if strings.Contains(renderErr.Error(), "PREFIX-MARKER") {
		t.Fatal("template source reached the error message")
	}
}

// selfRef is a model that points at itself. Nothing in the scan may hang on it.
type selfRef struct {
	// Next closes the loop.
	Next *selfRef
	// Payload is an any, so the gate cannot prune this type.
	Payload any
}

// TestASelfReferentialModelTerminates. A cycle is what a linked list with a
// back-pointer looks like, and a scan that hangs on it is a hung request.
func TestASelfReferentialModelTerminates(t *testing.T) {
	loop := &selfRef{Payload: "safe"}
	loop.Next = loop
	document := mustRender(t, page("p.html", `<p>{{.Payload}}</p>`), "p.html", loop)
	if !strings.Contains(document, "safe") {
		t.Fatalf("document = %q", document)
	}
}

// TestADeepCyclicModelStillTerminates drives the walk past the
// cycle-detection threshold, which is where the pointer bookkeeping starts.
func TestADeepCyclicModelStillTerminates(t *testing.T) {
	head := &selfRef{Payload: "safe"}
	tail := head
	for range 1200 {
		next := &selfRef{Payload: "safe"}
		tail.Next = next
		tail = next
	}
	tail.Next = head
	document := mustRender(t, page("p.html", `<p>{{.Payload}}</p>`), "p.html", head)
	if !strings.Contains(document, "safe") {
		t.Fatalf("document = %q", document)
	}
}

// TestAModelWithNoInterfaceFieldIsNeverWalked is the gate's observable
// consequence: the scan is a property of the TYPE, so a violation cannot hide
// behind a type that has no way to hold one.
func TestAModelWithNoInterfaceFieldIsNeverWalked(t *testing.T) {
	type plain struct {
		// Name and Count are the whole model; neither can hold a trust type.
		Name  string
		Count int
	}
	files := fstest.MapFS{"p.html": &fstest.MapFile{Data: []byte(`<p>{{.Name}}{{.Count}}</p>`)}}
	if got := mustRender(t, files, "p.html", plain{Name: "n", Count: 1}); !strings.Contains(got, "n1") {
		t.Fatalf("document = %q", got)
	}
}
