package kit_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// stayLimits are the lounge's own time limits, in its words.
const stayLimits = "a stay ends at its visitor's logout, or 30 days after it starts: the lounge deletes it"

// A store whose product keeps its retention itself (ADR 0006, amended
// 2026-09-29): kit.RetentionByProduct. The lounge ends its stays by its
// own rules; kit runs no retention there and asks for none — nor for a
// subject —, the register says the product keeps it, in its words, and the
// fields keep every other promise: redacted, exported and erased with
// their person.

// Lounge keeps its visitors' stays, and ends them itself.
var Lounge = kit.NewService("lounge", "Keeps its visitors' stays, and ends them itself.")

// Stay is one stay: whom it is about, and what they left.
type Stay struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Note  string `json:"note" kit:"personal"`
	Room  string `json:"room"`
}

// Doodle is what a visitor drew on the wall, about nobody kit can find.
type Doodle struct {
	ID      string `json:"id"`
	Drawing string `json:"drawing" kit:"personal"`
}

var (
	Stays = Lounge.Store("stays", func(v Stay) string { return v.ID },
		kit.RetentionByProduct(stayLimits),
		kit.Purpose("Welcome a visitor back"))
	Doodles = Lounge.Store("doodles", func(d Doodle) string { return d.ID },
		kit.RetentionByProduct(""))
	// Guestbook keeps personal data and says nothing: kit warns of it.
	Guestbook = Lounge.Store("guestbook", func(d Doodle) string { return d.ID })

	ExportVisitor = Lounge.Command("export-visitor", func(ctx context.Context, email string) (kit.PersonalData, error) {
		return kit.Export(ctx, email)
	})
	ForgetVisitor = Lounge.Command("forget-visitor", func(ctx context.Context, email string) (kit.Erasure, error) {
		return kit.Erase(ctx, "the visitor asked", email)
	})
)

// startLounge runs the lounge in dev, where keeps says — kit.InMemory(),
// or a data directory.
func startLounge(t *testing.T, keeps kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	app := kit.NewApp("lounge", Lounge).With(keeps, kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev),
		kit.Analyze(false), kit.Logs(io.Discard))
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

// kit asks a store whose product keeps its retention for neither a
// retention nor a subject, and runs no loop for it; the model says the
// product keeps it, in its words. A store that says nothing is still
// warned of.
func TestTheProductKeepsItsRetention(t *testing.T) {
	app := startLounge(t, kit.InMemory())
	g := app.Graph()
	var warned []string
	for _, d := range g.Diagnostics {
		if d.Severity == "warning" {
			warned = append(warned, d.Message)
		}
	}
	said := strings.Join(warned, "\n")
	for _, store := range []string{"lounge/store/stays", "lounge/store/doodles"} {
		if strings.Contains(said, store) {
			t.Errorf("kit warns of %s, whose product keeps its retention:\n%s", store, said)
		}
	}
	for _, want := range []string{"lounge/store/guestbook keeps personal data but says nowhere for how long", "lounge/store/guestbook keeps personal data, but no field tagged subject"} {
		if !strings.Contains(said, want) {
			t.Errorf("kit does not warn %q:\n%s", want, said)
		}
	}
	p := g.Node("lounge/store/stays").Store.Privacy
	if p == nil || p.ByProduct == nil || p.ByProduct.Limits != stayLimits || p.Erase != nil || p.Delete != nil || p.Subject != "/email" {
		t.Fatalf("the stays' privacy: %+v", p)
	}
	if d := g.Node("lounge/store/doodles").Store.Privacy; d == nil || d.ByProduct == nil || d.ByProduct.Limits != "" {
		t.Fatalf("the doodles' privacy: %+v", d)
	}
	for _, l := range g.Runtime.Loops {
		if l.Kind == model.LoopRetention {
			t.Errorf("kit runs a retention in the lounge: %+v", l)
		}
	}
}

// The register says the product keeps the store's retention — its words
// as the time limits of point (f) — and lists neither a retention nor a
// subject among its gaps.
func TestTheRegisterSaysTheProductKeepsTheRetention(t *testing.T) {
	app := startLounge(t, kit.InMemory())
	lines := map[string]model.RegisterStore{}
	for _, l := range privacyOf(t, app).Register.Stores {
		lines[l.Store] = l
	}
	stays, doodles := lines["lounge/store/stays"], lines["lounge/store/doodles"]
	if stays.ByProduct == nil || stays.ByProduct.Limits != stayLimits || len(stays.Gaps) != 0 {
		t.Errorf("the stays' line: %+v", stays)
	}
	if doodles.ByProduct == nil || !equalGaps(doodles.Gaps, []string{model.GapPurpose}) {
		t.Errorf("the doodles' line: %+v", doodles)
	}
	if g := lines["lounge/store/guestbook"].Gaps; !equalGaps(g, []string{model.GapPurpose, model.GapRetention, model.GapSubject}) {
		t.Errorf("the guestbook's gaps: %v", g)
	}
}

func equalGaps(got, want []string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// A person's data stays theirs where the product keeps the retention: it
// is sealed at rest, exported — the store's retention said in the
// product's words — and erased with them.
func TestAStoreTheProductRetainsIsStillAPersons(t *testing.T) {
	dir := t.TempDir()
	startLounge(t, kit.DataDir(dir))
	ctx := t.Context()
	must(t, Stays.Insert(ctx, Stay{ID: "v1", Email: "ada@x.dev", Note: "ada's note", Room: "blue"}))
	data, err := ExportVisitor.Dispatch(ctx, "ada@x.dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Stores) != 1 || len(data.Stores[0].Records) != 1 || !strings.Contains(data.Stores[0].Retention, stayLimits) ||
		!bytes.Contains(data.Stores[0].Records[0], []byte("ada's note")) {
		t.Fatalf("ada's export: %+v", data.Stores)
	}
	if files := filesHolding(t, os.DirFS(dir), "ada's note", "ada@x.dev"); len(files) > 0 {
		t.Errorf("her data rests in clear in %v", files)
	}
	if _, err := ForgetVisitor.Dispatch(ctx, "ada@x.dev"); err != nil {
		t.Fatal(err)
	}
	v, err := Stays.Get(ctx, "v1")
	if err != nil || v.Note != "" || v.Email != "" || v.Room != "blue" {
		t.Fatalf("after her erasure: %+v, %v", v, err)
	}
}

// A store's retention is kit's or the product's: both is refused, and so
// is the product's given twice.
func TestTheProductsRetentionIsItsAlone(t *testing.T) {
	svc := kit.NewService("both-retentions", "")
	svc.Store("reports", func(r Report) string { return r.ID },
		kit.RetentionByProduct("the desk purges its reports"), kit.EraseAfter(time.Hour, Report.Closed))
	svc.Store("twice", func(r Report) string { return r.ID },
		kit.RetentionByProduct("one"), kit.RetentionByProduct("two"))
	err := kit.NewApp("both", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	for _, want := range []string{
		`store "reports" says the product keeps its retention (kit.RetentionByProduct) and gives kit one`,
		`store "twice" declares kit.RetentionByProduct twice`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report lacks %q:\n%s", want, err)
		}
	}
}
