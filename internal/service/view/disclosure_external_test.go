package view_test

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcview "github.com/kitsunium/sdk/internal/service/view"
)

// The four secrets a render error must never carry to a third party. Each one
// is a distinct disclosure class, and html/template's own diagnostic contains
// three of them in a single string:
//
//	template: /srv/app/web/admin/page.html:1:7: executing "…" at <.User.Name>:
//	nil pointer evaluating interface {}.Name
const (
	// secretPath is the template's location in the tree — the application's
	// internal structure, and a map of the deployment.
	secretPath string = "vault/internal-admin-console.html"
	// secretMarkup is a fragment of the template's own source.
	secretMarkup string = "ZZ-TEMPLATE-BODY-ZZ"
	// secretExpression is the field chain the template dereferences.
	secretExpression string = "SecretField"
	// secretDatum is the caller's own data, rendered before the failure.
	secretDatum string = "QQ-USER-DATA-QQ"

	// unsafeValueText is the payload the UnsafeValue error must never echo. It
	// is the caller's data, and a refusal that quotes it has re-created the
	// leak the public/private split exists to close.
	unsafeValueText string = "WW-UNSAFE-PAYLOAD-WW"
)

// TestARenderFailureDisclosesNothingToAThirdParty is the domain's security
// rule for error text, stated executably.
//
// A Public is written to third parties. Everything a template engine handles
// is either the application's internal structure or somebody's data, so a
// Public carries neither — and the wrapped error's rendered form, which a
// careless handler will send verbatim, carries neither either.
func TestARenderFailureDisclosesNothingToAThirdParty(t *testing.T) {
	body := secretMarkup + `{{.Payload}}{{.Missing.` + secretExpression + `}}`
	files := fstest.MapFS{secretPath: &fstest.MapFile{Data: []byte(body)}}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	document, renderErr := renderer.Render(t.Context(),
		secretPath, struct {
			// Payload is rendered before the failure, so it is data that the
			// engine has already touched when the error is built.
			Payload string
			// Missing is nil, which is what makes the field chain fail.
			Missing any
		}{Payload: secretDatum})
	if renderErr == nil {
		t.Fatal("the render was expected to fail")
	}
	if document != nil {
		t.Fatalf("document = %q, want nil", document)
	}
	assertNoSecret(t, "Error()", renderErr.Error())
	assertNoSecret(t, "PublicOf", errs.PublicOf(renderErr))
	assertNoSecret(t, "PrivateOf", errs.PrivateOf(renderErr))
}

// TestTheOperatorStillGetsTheDiagnostic is the other half, and it is what
// makes the first half acceptable rather than merely quiet.
//
// The engine's message is not destroyed — it travels as a FIELD, which
// ADR 0005 keeps out of err.Error(). An operator reading structured logs has
// everything; a browser has one sentence.
func TestTheOperatorStillGetsTheDiagnostic(t *testing.T) {
	body := `{{.Missing.` + secretExpression + `}}`
	files := fstest.MapFS{secretPath: &fstest.MapFile{Data: []byte(body)}}
	renderer, err := svcview.NewHTML(coreview.Config{FS: files})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	_, renderErr := renderer.Render(t.Context(), secretPath, struct{ Missing any }{})
	if renderErr == nil {
		t.Fatal("the render was expected to fail")
	}
	diagnostic := fieldMap(errs.FieldsOf(renderErr))["engine_error"]
	if diagnostic == "" {
		t.Fatal("the engine diagnostic was dropped entirely; an operator has nothing to debug with")
	}
	if !strings.Contains(diagnostic, secretExpression) {
		t.Fatalf("engine_error = %q, expected it to name the failing expression", diagnostic)
	}
	if strings.Contains(renderErr.Error(), diagnostic) {
		t.Fatal("the field was folded into Error(); Fields must stay log-side (ADR 0005)")
	}
}

// TestAConstructionFailureDisclosesNothingEither. A template tree is refused
// at boot, so its diagnostic is less likely to be forwarded — "less likely" is
// not a security boundary, so the same rule applies.
func TestAConstructionFailureDisclosesNothingEither(t *testing.T) {
	files := fstest.MapFS{secretPath: &fstest.MapFile{Data: []byte(secretMarkup + `{{if}}`)}}
	_, err := svcview.NewHTML(coreview.Config{FS: files})
	if err == nil {
		t.Fatal("an unparseable tree was accepted")
	}
	assertNoSecret(t, "Error()", err.Error())
	assertNoSecret(t, "PublicOf", errs.PublicOf(err))
	assertNoSecret(t, "PrivateOf", errs.PrivateOf(err))
}

// TestAnUnsafeValueErrorNamesThePathAndTheTypeButNeverTheValue.
//
// A message names the rule and the bound and never the value — validation's
// rule (ADR 0046), which applies here for the same reason: the path is
// structure the developer owns, the value is data somebody else does.
func TestAnUnsafeValueErrorNamesThePathAndTheTypeButNeverTheValue(t *testing.T) {
	renderer, err := svcview.NewHTML(coreview.Config{FS: page("p.html", `<p>x</p>`)})
	if err != nil {
		t.Fatalf("NewHTML: %v", err)
	}
	model := struct {
		// Boot carries a refused trust type, at a known path.
		Boot template.JS
	}{Boot: template.JS(unsafeValueText)}
	_, unsafeErr := renderer.Render(t.Context(), "p.html", model)
	if unsafeErr == nil {
		t.Fatal("the unsafe model was accepted")
	}
	fields := fieldMap(errs.FieldsOf(unsafeErr))
	if fields["path"] != "data.Boot" {
		t.Fatalf("path = %q, want data.Boot", fields["path"])
	}
	if fields["type"] != "html/template.JS" {
		t.Fatalf("type = %q, want html/template.JS", fields["type"])
	}
	if strings.Contains(unsafeErr.Error(), unsafeValueText) {
		t.Fatalf("the offending VALUE reached Error(): %q", unsafeErr.Error())
	}
	for _, field := range errs.FieldsOf(unsafeErr) {
		if strings.Contains(field.StringValue(), unsafeValueText) {
			t.Fatalf("the offending VALUE reached field %q", field.Key())
		}
	}
}

// assertNoSecret fails when text carries any of the four disclosure classes.
func assertNoSecret(tb testing.TB, label, text string) {
	tb.Helper()
	for _, secret := range []string{secretPath, secretMarkup, secretExpression, secretDatum} {
		if strings.Contains(text, secret) {
			tb.Fatalf("%s leaked %q: %q", label, secret, text)
		}
	}
}
