package kit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// What kit shows of the personal data (ADR 0006 §9, §10, §12): the model,
// what a module discovers, the redaction everywhere, the start's problems
// and warnings, and the Studio's Privacy page.

// The model carries the classification: each field's class and options,
// and each store's subject, purpose and retention.
func TestTheGraphCarriesTheClassification(t *testing.T) {
	g := startPrivacy(t).Graph()
	pv := storePrivacyOf(t, g, "desk/store/reports")
	if pv.Erase == nil || pv.Delete == nil {
		t.Fatalf("reports' retention: %+v", pv)
	}
	for what, ok := range map[string]bool{
		"subject":   pv.Subject == "/email",
		"purpose":   pv.Purpose != "",
		"erase":     pv.Erase.After == (90 * 24 * time.Hour).String(),
		"since":     pv.Erase.Since != nil,
		"delete":    pv.Delete.Setting == "reports-delete-after",
		"anonymise": pv.Anonymise != nil,
		"held":      pv.Held != nil,
	} {
		if !ok {
			t.Errorf("reports' privacy, %s: %+v", what, pv)
		}
	}
	checkFieldMarks(t, g.Node("desk/store/reports").Store.Entity.Fields)
}

// The model carries a hold until an instant, kit's own stores, and the
// retention loops.
func TestTheGraphCarriesTheHoldsAndTheLoops(t *testing.T) {
	g := startPrivacy(t).Graph()
	inv := storePrivacyOf(t, g, "people/store/invoices")
	if inv.HeldUntil == nil || inv.HeldUntil.Reason != "commercial code: 10 years" || inv.Erase == nil || inv.Erase.At == nil {
		t.Errorf("invoices' privacy: %+v", inv)
	}
	if g.Node("kit.privacy/store/journal") == nil || g.Node("kit.privacy/store/holds") == nil {
		t.Error("kit's own stores are not in the graph")
	}
	if loops := retentionLoopNames(g); !slices.Equal(loops, []string{"desk/store/reports retention", "people/store/invoices retention"}) {
		t.Errorf("retention loops: %v", loops)
	}
}

// storePrivacyOf is what the graph says of a store's personal data.
func storePrivacyOf(t *testing.T, g *model.Graph, id string) *model.StorePrivacy {
	t.Helper()
	n := g.Node(id)
	if n == nil || n.Store == nil || n.Store.Privacy == nil {
		t.Fatalf("%s: %+v", id, n)
	}
	return n.Store.Privacy
}

// fieldMarks is what the model says of a field's classification: its class,
// and the marks it carries, in a fixed order.
type fieldMarks struct {
	class string
	marks string
}

// marksOf spells the marks the model gives a field, in a fixed order.
func marksOf(f *model.Field) string {
	var marks []string
	for _, m := range []struct {
		on   bool
		name string
	}{{f.Subject, "subject"}, {f.Moderated, "moderated"}, {f.Erased, "erased"}, {f.Sealed, "sealed"}} {
		if m.on {
			marks = append(marks, m.name)
		}
	}
	return strings.Join(marks, ",")
}

// checkFieldMarks checks each report field carries what its tag says —
// and that nothing is sealed yet.
func checkFieldMarks(t *testing.T, fields []model.Field) {
	t.Helper()
	got := map[string]fieldMarks{}
	for i := range fields {
		got[fields[i].Name] = fieldMarks{fields[i].Class, marksOf(&fields[i])}
	}
	for _, c := range []struct {
		name string
		want fieldMarks
	}{
		{"category", fieldMarks{class: model.ClassPublic}},
		{"explanation", fieldMarks{class: model.ClassPersonal, marks: "moderated"}},
		{"email", fieldMarks{class: model.ClassPersonal, marks: "subject"}},
		{"health", fieldMarks{class: model.ClassSpecial}},
		{"trackHash", fieldMarks{class: model.ClassSecret}},
		{"erasedAt", fieldMarks{marks: "erased"}},
		{"closedAt", fieldMarks{}},
	} {
		if got[c.name] != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got[c.name], c.want)
		}
	}
}

// retentionLoopNames lists the retention loops of the graph's runtime.
func retentionLoopNames(g *model.Graph) []string {
	var out []string
	for _, l := range g.Runtime.Loops {
		if l.Kind == model.LoopRetention {
			out = append(out, l.Name)
		}
	}
	return out
}

// A module discovers the moderated and the special fields.
func TestAModuleDiscoversTheMarkedFields(t *testing.T) {
	app := startPrivacy(t)
	got := app.Fields(kit.Moderated)
	want := []kit.FieldRefValue{
		{Store: "desk/store/reports", Path: "/explanation", Class: "personal", Subject: "/email"},
		{Store: "desk/store/reports", Path: "/notes/*/body", Class: "personal", Subject: "/email"},
		{Store: "people/store/customers", Path: "/pseudonym", Class: "personal", Subject: "/id"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Fields(Moderated) = %+v", got)
	}
	if special := app.Fields(kit.Special); len(special) != 1 || special[0].Path != "/health" {
		t.Errorf("Fields(Special) = %+v", special)
	}
	if _, err := app.Records("desk/store/nope"); err == nil {
		t.Error("a store the app does not mount has records")
	}
}

// A module reads a record through the untyped port, which never returns a
// secret member, and clears the fields App.Fields names.
func TestAModuleReadsAndErasesThroughRecords(t *testing.T) {
	app := startPrivacy(t)
	seed(t)
	recs, recsErr := app.Records("people/store/customers")
	if recsErr != nil {
		t.Fatal(recsErr)
	}
	raw, err := recs.Get(t.Context(), "u-bob")
	if err != nil || len(mentions(string(raw), "phc-bob")) > 0 || !bytes.Contains(raw, []byte("bobby")) {
		t.Errorf("Records.Get = %s, %v", raw, err)
	}
	rr, rrErr := app.Records("desk/store/reports")
	if rrErr != nil {
		t.Fatal(rrErr)
	}
	must(t, rr.EraseFields(t.Context(), "r2", "moderated away", "/explanation"))
	if r, err := Reports.Get(t.Context(), "r2"); err != nil || r.Explanation != "" || r.Name != "Bob" {
		t.Errorf("after EraseFields: %+v", r)
	}
	if err := rr.EraseFields(t.Context(), "r2", "x", "/category/nope"); err == nil {
		t.Error("a path naming no classified field was accepted")
	}
}

// A personal member is redacted wherever kit shows it: a payload, the log
// ring, and the terminal's lines — a struct logged whole included.
func TestPersonalDataIsRedactedEverywhere(t *testing.T) {
	terminal := &lockedBuffer{}
	app := startPrivacy(t, kit.Logs(terminal))
	r := call(t, app, "POST /reports", Report{ID: "r9", Category: "spam", Email: "zoe@x.dev", Name: "Zoé", Explanation: "zoe's words"})
	if r.status != http.StatusOK {
		t.Fatalf("file: %d %s", r.status, r.body)
	}
	for _, s := range traceOf(t, app, r).Spans {
		if s.Payload != nil && len(mentions(string(s.Payload.Request)+string(s.Payload.Response), "zoe@x.dev", "zoe's words")) > 0 {
			t.Errorf("a payload shows personal data: %s %s", s.Payload.Request, s.Payload.Response)
		}
	}
	logs := string(call(t, app, "GET /_kit/api/logs", noBody).body)
	if len(mentions(logs, "zoe@x.dev", "hunter2")) > 0 || !strings.Contains(logs, "[redacted]") {
		t.Errorf("the log ring: %s", logs)
	}
	eventually(t, "the terminal line", func() bool { return strings.Contains(terminal.String(), "a report was filed") })
	line := terminal.String()
	if len(mentions(line, "zoe@x.dev", "Zoé", "hunter2")) > 0 || len(mentions(line, "[redacted]", "spam")) != 2 {
		t.Errorf("the terminal: %s", line)
	}
	if items := call(t, app, "GET /_kit/api/items?store=desk/store/reports", noBody); bytes.Contains(items.body, []byte("zoe@x.dev")) {
		t.Errorf("the data browser shows personal data: %s", items.body)
	}
}

type badTags struct {
	ID string `json:"id"`
	A  string `json:"a" kit:"privat"`
	B  string `json:"b" kit:"personal,secret"`
	C  string `json:"c" kit:"special,plain"`
	D  string `json:"d" kit:"secret,moderated"`
	E  string `json:"e" kit:"subject"`
	F  string `json:"f" kit:"subject"`
	G  string `json:"g" kit:"erased"`
	H  string `json:"h" kit:"history=0"`
}

type badRequest struct {
	Who string `json:"who" kit:"public,subject"`
}

// Every refusal of the grammar and of the options is a declaration problem
// at the declaration's position, all reported together.
func TestPrivacyDeclarationProblemsAreReportedTogether(t *testing.T) {
	svc := kit.NewService("bad-privacy", "")
	svc.Store("tags", func(b badTags) string { return b.ID })
	svc.Store("options", func(r Report) string { return r.ID },
		kit.EraseAfter(time.Hour, Invoice.keptUntil),
		kit.EraseAt(Report.Closed), kit.EraseAt(Report.Closed),
		kit.DeleteAfter(-time.Hour, Report.Closed),
		kit.Anonymise[Report](nil))
	svc.Endpoint("POST /bad", func(context.Context, badRequest) (kit.EmptyValue, error) { return kit.EmptyValue{}, nil })
	app := kit.NewApp("bad", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	err := app.Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	for _, want := range []string{
		`unknown word "privat"`, "two classes, personal and secret", "never plain", "nobody moderates it",
		"two subject fields", "kit stamps a time.Time", "from 1 to 100", "cannot be public",
		"kit.EraseAfter reads kit_test.Invoice", "declares erase twice", "the delay of kit.DeleteAfter must be positive",
		"kit.Anonymise has a nil function",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report lacks %q:\n%s", want, err)
		}
	}
	for _, d := range de.Diagnostics {
		if d.Source == nil || d.Source.File != "framework/internal/kit/privacy_view_test.go" {
			t.Errorf("a problem without its position: %+v", d)
		}
	}
}

// In dev, kit warns of a field that reads like personal data with no class,
// and of a store that keeps personal data with no retention; a start
// outside dev keeps quiet, and config says it all.
func TestPrivacyWarningsAreSaidInDev(t *testing.T) {
	warnings := func(g *model.Graph) string {
		var out []string
		for _, d := range g.Diagnostics {
			if d.Severity == "warning" {
				out = append(out, d.Message)
			}
		}
		return strings.Join(out, "\n")
	}
	app := startPrivacy(t)
	said := warnings(app.Graph())
	for _, want := range []string{"Invoice.ContactPhone reads like personal data", "people/store/customers keeps personal data but says nowhere for how long"} {
		if !strings.Contains(said, want) {
			t.Errorf("dev does not warn %q:\n%s", want, said)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, app.Stop(ctx))
	prod := kit.NewApp("vigie", Desk, People).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
	must(t, prod.Start(t.Context()))
	defer func() {
		if err := prod.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	if said := warnings(prod.Graph()); strings.Contains(said, "reads like personal data") {
		t.Errorf("production warns: %s", said)
	}
}
