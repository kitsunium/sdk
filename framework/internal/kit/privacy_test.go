package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// The privacy product (ADR 0006): a desk of reports people file, accounts
// and invoices. Its declarations are package-level, as a product writes
// them.

var Desk = kit.NewService("desk", "Notices people file, for the privacy tests.")

// Report is a notice a person filed.
type Report struct {
	ID          string     `json:"id"`
	Category    string     `json:"category" kit:"public"`
	Explanation string     `json:"explanation" kit:"personal,moderated"`
	Email       string     `json:"email,omitempty" kit:"subject"`
	Name        string     `json:"name,omitempty" kit:"personal"`
	Health      string     `json:"health,omitempty" kit:"special"`
	TrackHash   string     `json:"trackHash,omitempty" kit:"secret"`
	Birth       *time.Time `json:"birth,omitempty" kit:"personal"`
	BirthYear   int        `json:"birthYear,omitempty"`
	Notes       []Note     `json:"notes,omitempty"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	ErasedAt    *time.Time `json:"erasedAt,omitempty" kit:"erased"`
}

// Note is a message on a report.
type Note struct {
	Body   string `json:"body" kit:"personal,moderated"`
	Public string `json:"public,omitempty"`
}

func (r Report) Key() string { return r.ID }

// Closed is when the report was closed: false while it is open.
func (r Report) Closed() (time.Time, bool) {
	if r.ClosedAt == nil {
		return time.Time{}, false
	}
	return *r.ClosedAt, true
}

// keepBirthYear keeps the year of an erased report's birth date. A report
// filed under "boom" makes it panic, as a bug of the product's would.
func keepBirthYear(r *Report) {
	if r.Category == "boom" {
		panic("a bug in the product's Anonymise")
	}
	if r.Birth != nil {
		r.BirthYear = r.Birth.Year()
	}
}

var reportsDeleteAfter = Desk.Setting("reports-delete-after", 365*24*time.Hour)

var Reports = Desk.Store("reports", Report.Key,
	kit.EraseAfter(90*24*time.Hour, Report.Closed),
	kit.DeleteAfter(reportsDeleteAfter, Report.Closed),
	kit.Anonymise(keepBirthYear),
	kit.Purpose("Handle the notices people file, and answer them"))

var People = kit.NewService("people", "Customers and invoices, for the privacy tests.")

// Customer is a person's account: its key is their ID, their subject too.
type Customer struct {
	ID        string `json:"id" kit:"subject"`
	Email     string `json:"email" kit:"personal"`
	Password  string `json:"password" kit:"secret"`
	Pseudonym string `json:"pseudonym" kit:"personal,moderated,plain"`
}

var Customers = People.Store("customers", func(a Customer) string { return a.ID },
	kit.DeleteOnErasure(), kit.Purpose("Sign people in"))

// Invoice is kept ten years, whatever its customer asks.
type Invoice struct {
	ID        string    `json:"id"`
	Customer  string    `json:"customer" kit:"subject"`
	Address   string    `json:"address" kit:"personal"`
	Amount    int       `json:"amount"`
	KeepUntil time.Time `json:"keepUntil"`
	// ContactPhone reads like personal data, and says nothing: warned of.
	ContactPhone string `json:"contactPhone,omitempty"`
}

func (i Invoice) keptUntil() (time.Time, bool) { return i.KeepUntil, !i.KeepUntil.IsZero() }

var Invoices = People.Store("invoices", func(i Invoice) string { return i.ID },
	kit.HeldUntil(Invoice.keptUntil, "commercial code: 10 years"),
	kit.EraseAt(Invoice.keptUntil),
	kit.Purpose("Bill the customers"))

// Identities names a person by their identities.
type Identities struct {
	IDs []string `json:"ids" kit:"personal"`
}

var (
	// A person's rights, run in process by the desk: internal, as a
	// command or a query nobody exposes is.
	DeskExport = Desk.Query("export", ExportData)
	DeskErase  = Desk.Command("erase", EraseData)
	DeskHold   = Desk.Command("hold", HoldData)
	_          = Desk.Endpoint("POST /reports", FileReport)
)

// ExportData gives a person their data.
func ExportData(ctx context.Context, in Identities) (kit.PersonalData, error) {
	return kit.Export(ctx, in.IDs...)
}

// EraseData erases a person's data.
func EraseData(ctx context.Context, in Identities) (kit.Erasure, error) {
	return kit.Erase(ctx, "erasure requested by the person", in.IDs...)
}

// HoldData holds a person's records.
func HoldData(ctx context.Context, in Identities) (kit.EmptyValue, error) {
	return kit.EmptyValue{}, kit.HoldSubject(ctx, "an authority's request", in.IDs...)
}

// FileReport files a report, and logs it whole.
func FileReport(ctx context.Context, r Report) (Report, error) {
	logger.Info(ctx, kit.Log(ctx), "a report was filed", logger.Any("report", r), logger.String("password", "hunter2"))
	return r, Reports.Insert(ctx, r)
}

// startPrivacy runs the privacy product in memory, in dev.
func startPrivacy(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	return startPrivacyOn(t, append([]kit.AppConfigurer{kit.InMemory()}, opts...)...)
}

// startPrivacyOn runs the privacy product in dev, where opts say.
func startPrivacyOn(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	app := kit.NewApp("vigie", Desk, People).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return app
}

var epoch = time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time {
	return new(epoch.Add(d))
}

// ann has two identities: her e-mail on reports, her ID on accounts and
// invoices. bob has one of each.
func seed(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	must(t, Reports.Insert(ctx, Report{
		ID: "r1", Category: "spam", Explanation: "ann's words", Email: "ann@x.dev", Name: "Ann", Health: "asthma",
		TrackHash: "h1", Birth: at(-40 * 365 * 24 * time.Hour), Notes: []Note{{Body: "ann's note", Public: "kept"}},
	}))
	must(t, Reports.Insert(ctx, Report{ID: "r2", Category: "hate", Explanation: "bob's words", Email: "bob@x.dev", Name: "Bob"}))
	must(t, Customers.Insert(ctx, Customer{ID: "u-ann", Email: "ann@x.dev", Password: "phc-ann", Pseudonym: "annie"}))
	must(t, Customers.Insert(ctx, Customer{ID: "u-bob", Email: "bob@x.dev", Password: "phc-bob", Pseudonym: "bobby"}))
	must(t, Invoices.Insert(ctx, Invoice{ID: "i1", Customer: "u-ann", Address: "1 rue de la Paix", Amount: 12, KeepUntil: epoch.Add(10 * 365 * 24 * time.Hour)}))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// privacyOf reads the Studio's Privacy page.
func privacyOf(t *testing.T, app *kit.App) model.Privacy {
	t.Helper()
	r := call(t, app, "GET /_kit/api/privacy", noBody)
	if r.status != http.StatusOK {
		t.Fatalf("privacy: %d %s", r.status, r.body)
	}
	var p model.Privacy
	r.json(t, &p)
	return p
}

// retentionLoop reads one loop of the daemon.
func retentionLoop(t *testing.T, app *kit.App, name string) model.Loop {
	t.Helper()
	for _, l := range app.Graph().Runtime.Loops {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no loop %s", name)
	return model.Loop{}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, rawErr := json.Marshal(v)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	return raw
}

// mentions lists the words s contains.
func mentions(s string, words ...string) []string {
	var out []string
	for _, w := range words {
		if strings.Contains(s, w) {
			out = append(out, w)
		}
	}
	return out
}

// failedWith is err's kit code and message; empty when err is not a kit
// error.
func failedWith(err error) (code, message string) {
	var ke *kit.Error
	if errors.As(err, &ke) {
		return ke.Code, ke.Message
	}
	return "", ""
}
