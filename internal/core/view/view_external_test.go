package view_test

import (
	"context"
	"html/template"
	"reflect"
	"strings"
	"testing"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxPublicRunes is rule 4's ceiling on a Public string.
const maxPublicRunes int = 120

// portPrefix is the 0.2.27.* block this package owns (ADR 0058).
const portPrefix errs.Code = 0x00_02_1B_00

// prefixMask keeps MM.LL.PP and drops the serial byte.
const prefixMask errs.Code = 0xFF_FF_FF_00

// stubRenderer is a Renderer double. It exists to prove the port is
// implementable outside this package — which is what "frozen at two methods"
// has to mean for it to be worth anything.
type stubRenderer struct{}

// Render returns a fixed document.
func (stubRenderer) Render(_ context.Context, _ string, _ any) ([]byte, error) {
	return []byte("<p>ok</p>"), nil
}

// ContentType reports the HTML media type.
func (stubRenderer) ContentType() string { return coreview.ContentTypeHTML }

// TestATwoMethodDoubleStillSatisfiesRenderer is the ADR 0039 freeze guard.
//
// pkg/v1/view aliases this interface and Go interfaces are structural: a third
// method would break every downstream implementation at compile time with no
// deprecation window. This test stops compiling the moment one is added.
func TestATwoMethodDoubleStillSatisfiesRenderer(t *testing.T) {
	renderer := coreview.Renderer(stubRenderer{})
	document, err := renderer.Render(t.Context(), "any", nil)
	if err != nil {
		t.Fatalf("stub render: %v", err)
	}
	if string(document) != "<p>ok</p>" {
		t.Fatalf("stub document = %q", document)
	}
	if renderer.ContentType() != coreview.ContentTypeHTML {
		t.Fatalf("stub content type = %q", renderer.ContentType())
	}
	if reflect.TypeFor[coreview.Renderer]().NumMethod() != 2 {
		t.Fatalf("Renderer has %d methods, want 2 (ADR 0039 freeze)",
			reflect.TypeFor[coreview.Renderer]().NumMethod())
	}
}

// TestContentTypeCarriesTheCharset pins the security half of the constant.
//
// A response whose Content-Type carries no charset is sniffed by the browser,
// and a document sniffed as UTF-7 can smuggle markup past an escaper that
// judged the bytes as UTF-8.
func TestContentTypeCarriesTheCharset(t *testing.T) {
	if !strings.Contains(coreview.ContentTypeHTML, "charset=utf-8") {
		t.Fatalf("ContentTypeHTML = %q, want a utf-8 charset", coreview.ContentTypeHTML)
	}
}

// TestTrustedHTMLIsLiterallyTheStdlibType is the whole reason TrustedHTML is
// an alias rather than a defined type.
//
// html/template recognises its trust types by an exact type switch. A
// `type TrustedHTML string` would be escaped like any other string, so a trust
// type that does not confer trust would be worse than none — callers stop
// looking for the reason their markup disappeared.
func TestTrustedHTMLIsLiterallyTheStdlibType(t *testing.T) {
	if reflect.TypeFor[coreview.TrustedHTML]() != reflect.TypeFor[template.HTML]() {
		t.Fatal("TrustedHTML is not html/template.HTML; escaping will not be bypassed")
	}
	if coreview.TrustHTML("<b>hi</b>") != template.HTML("<b>hi</b>") {
		t.Fatal("TrustHTML did not produce an html/template.HTML value")
	}
}

// TestTrustHTMLIsTheOnlyTrustConstructorTheDomainOffers records the surface
// deliberately: six of html/template's seven trust types have NO spelling
// here, and the seventh has exactly one.
func TestTrustHTMLIsTheOnlyTrustConstructorTheDomainOffers(t *testing.T) {
	for _, banned := range []string{"TrustCSS", "TrustJS", "TrustJSStr", "TrustURL", "TrustSrcset", "TrustHTMLAttr"} {
		if exported(t, banned) {
			t.Fatalf("view.%s exists; the domain must offer no spelling for it", banned)
		}
	}
	if !exported(t, "TrustHTML") {
		t.Fatal("view.TrustHTML is missing; the one trusted spelling must have a name")
	}
}

// exported reports whether the package declares name, by parsing its own
// source. Reflection cannot enumerate a package, so the source is the oracle.
func exported(tb testing.TB, name string) bool {
	tb.Helper()
	return declaredIdentifiers(tb)[name]
}

// TestEveryPublicObeysRuleFour walks the sentinels and checks the wire-safe
// half against its two hard limits.
func TestEveryPublicObeysRuleFour(t *testing.T) {
	for _, sentinel := range portSentinels() {
		public := sentinel.Public()
		if public == "" {
			t.Fatalf("%s has an empty Public", sentinel.Reason())
		}
		if count := len([]rune(public)); count > maxPublicRunes {
			t.Fatalf("%s Public is %d runes, over the %d ceiling", sentinel.Reason(), count, maxPublicRunes)
		}
		if strings.ContainsAny(public, "\n\r") {
			t.Fatalf("%s Public contains a newline", sentinel.Reason())
		}
	}
}

// TestNoPublicNamesATemplateAPathOrAValue is the domain's non-disclosure rule
// stated at the sentinel level.
//
// Everything a template engine touches is either the application's internal
// structure — a file path, a template name, a line number — or somebody's
// data. A Public is written to third parties, so it carries neither.
func TestNoPublicNamesATemplateAPathOrAValue(t *testing.T) {
	// leaky are substrings that only ever appear in a Public by accident:
	// path separators, template delimiters and engine vocabulary.
	leaky := []string{"/", "\\", "{{", "}}", ".html", ".gohtml", "html/template", "line ", ":1:"}
	for _, sentinel := range portSentinels() {
		for _, needle := range leaky {
			if strings.Contains(sentinel.Public(), needle) {
				t.Fatalf("%s Public %q contains %q", sentinel.Reason(), sentinel.Public(), needle)
			}
		}
	}
}

// TestEverySentinelSitsInTheOwnedBlock is the ADR 0035 range check restated
// locally, so a stray code is caught in this package's own suite too.
func TestEverySentinelSitsInTheOwnedBlock(t *testing.T) {
	seen := map[errs.Code]string{}
	for _, sentinel := range portSentinels() {
		code := sentinel.Code()
		if code&prefixMask != portPrefix {
			t.Fatalf("%s carries %s, outside the 0.2.27.* block", sentinel.Reason(), code)
		}
		if previous, taken := seen[code]; taken {
			t.Fatalf("%s and %s both carry %s", previous, sentinel.Reason(), code)
		}
		seen[code] = sentinel.Reason()
	}
}

// TestReasonsAreScreamingSnakeOfTheirVarName mirrors the repo-wide errs audit
// so a rename that breaks the pairing fails here first, with a clearer message.
func TestReasonsAreScreamingSnakeOfTheirVarName(t *testing.T) {
	want := []struct {
		// reason is the SCREAMING_SNAKE identifier errs.Define was given.
		reason string
		// varName is the sentinel variable that must carry it.
		varName string
	}{
		{"VIEW_MISCONFIGURED", "ViewMisconfigured"},
		{"TEMPLATE_NOT_FOUND", "TemplateNotFound"},
		{"RENDER_FAILED", "RenderFailed"},
		{"RENDER_TOO_LARGE", "RenderTooLarge"},
		{"UNSAFE_VALUE", "UnsafeValue"},
		{"ENGINE_UNKNOWN", "EngineUnknown"},
		{"ENGINE_INVALID", "EngineInvalid"},
		{"DUPLICATE_ENGINE", "DuplicateEngine"},
	}
	if len(want) != len(portSentinels()) {
		t.Fatalf("the pairing table has %d entries for %d sentinels", len(want), len(portSentinels()))
	}
	declared := declaredIdentifiers(t)
	paired := map[string]bool{}
	for _, pair := range want {
		if !declared[pair.varName] {
			t.Fatalf("sentinel %s (%s) is not declared in this package", pair.varName, pair.reason)
		}
		paired[pair.reason] = true
	}
	for _, sentinel := range portSentinels() {
		if !paired[sentinel.Reason()] {
			t.Fatalf("sentinel reason %s is not in the pairing table", sentinel.Reason())
		}
	}
}

// portSentinels is every *errs.Error this package declares.
func portSentinels() []*errs.Error {
	return []*errs.Error{
		coreview.ViewMisconfigured,
		coreview.TemplateNotFound,
		coreview.RenderFailed,
		coreview.RenderTooLarge,
		coreview.UnsafeValue,
		coreview.EngineUnknown,
		coreview.EngineInvalid,
		coreview.DuplicateEngine,
	}
}
