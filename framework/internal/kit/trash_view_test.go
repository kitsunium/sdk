package kit_test

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// What the Studio sees of a store's trash (ADR 0178 C4, amended on
// 2026-10-15): GET /_kit/api/trash lists what the product deleted — each
// record's key, when it was deleted and when kit purges it, never its
// value —, a page at a time; a key that reads a personal, special or secret
// member is hidden, as the data browser hides that member.

var Bin = kit.NewService("bin", "Passes deleted into a trash, for the Studio's trash list (ADR 0178 C4).")

// Pass is keyed by its holder's email, the subject: its key is personal
// data.
type Pass struct {
	Email string `json:"email" kit:"subject"`
	Label string `json:"label"`
}

// Score keeps no revisions, so no trash.
type Score struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

var (
	Passes = Bin.Store("passes", func(b Pass) string { return b.Email }, kit.Revisions(2), kit.Purpose("Show the passes"))
	_      = Bin.Store("scores", func(c Score) string { return c.ID })
)

// startBin runs the passes in memory, in dev, on clk.
func startBin(t *testing.T, clk *clock.ManualClock) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	app := kit.NewApp("bin", Bin).With(kit.InMemory(), kit.Clock(clk),
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	run(t, app)
	return app
}

// trashPage is GET /_kit/api/trash of route's query, answered 200.
func trashPage(t *testing.T, app *kit.App, query string) model.StoreTrash {
	t.Helper()
	r := call(t, app, "GET /_kit/api/trash?"+query, noBody)
	if r.status != http.StatusOK {
		t.Fatalf("trash?%s: %d %s", query, r.status, r.body)
	}
	if leaked := mentions(string(r.body), "Annabel", "Quist", "Bob Hale", "code-", "value"); leaked != nil {
		t.Errorf("the trash list shows %v: %s", leaked, r.body)
	}
	var page model.StoreTrash
	r.json(t, &page)
	return page
}

// The trash list gives each record's key, deletion and purge instants, in
// key order, a page at a time: limit records from offset, next the offset
// of the page after, absent on the last.
func TestTheStudioListsTheTrash(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	app := startFolk(t, kit.InMemory(), kit.Clock(clk))
	ctx := t.Context()
	for i, id := range []string{"m3", "m1", "m2"} {
		must(t, Folks.Insert(ctx, Member{ID: id, Email: id + "@folk.test", Name: "Annabel Quist", Code: fmt.Sprintf("code-%d", i)}))
		must(t, Folks.Delete(ctx, id))
		clk.Advance(time.Hour)
	}

	first := trashPage(t, app, "store=folk/store/members&limit=2")
	if first.Store != "folk/store/members" || first.Total != 3 || first.Next != 2 || len(first.Records) != 2 {
		t.Fatalf("the first page: %+v", first)
	}
	want := map[string]time.Time{"m3": epoch, "m1": epoch.Add(time.Hour), "m2": epoch.Add(2 * time.Hour)}
	last := trashPage(t, app, "store=folk/store/members&limit=2&offset=2")
	if last.Total != 3 || last.Next != 0 || len(last.Records) != 1 {
		t.Fatalf("the last page: %+v", last)
	}
	var keys []string
	for _, rec := range slices.Concat(first.Records, last.Records) {
		keys = append(keys, rec.Key)
		if rec.Hidden || !rec.At.Equal(want[rec.Key]) || !rec.PurgeAt.Equal(want[rec.Key].Add(trashDays)) {
			t.Errorf("record %s: %+v, want deleted at %v, purged 30 days after", rec.Key, rec, want[rec.Key])
		}
	}
	if !slices.Equal(keys, []string{"m1", "m2", "m3"}) {
		t.Errorf("the trash lists %q, want the keys in order", keys)
	}
	if all := trashPage(t, app, "store=folk/store/members"); len(all.Records) != 3 || all.Next != 0 {
		t.Errorf("the default page: %+v", all)
	}
	if past := trashPage(t, app, "store=folk/store/members&offset=9"); past.Total != 3 || len(past.Records) != 0 || past.Records == nil {
		t.Errorf("a page past the end: %+v", past)
	}
}

// A key that reads the subject is hidden: the record is listed, with its
// instants, and the key the data browser would never show is not given.
func TestTheTrashListHidesAPersonalKey(t *testing.T) {
	app := startBin(t, clock.NewManualClock(epoch))
	must(t, Passes.Insert(t.Context(), Pass{Email: "ann@bin.test", Label: "gold"}))
	must(t, Passes.Delete(t.Context(), "ann@bin.test"))

	r := call(t, app, "GET /_kit/api/trash?store=bin/store/passes", noBody)
	if r.status != http.StatusOK || mentions(string(r.body), "ann@bin.test", "gold") != nil {
		t.Fatalf("the passes' trash: %d %s", r.status, r.body)
	}
	var page model.StoreTrash
	r.json(t, &page)
	if len(page.Records) != 1 || !page.Records[0].Hidden || page.Records[0].Key != "" ||
		!page.Records[0].At.Equal(epoch) || !page.Records[0].PurgeAt.Equal(epoch.Add(trashDays)) {
		t.Errorf("the pass's record: %+v, want its key hidden and its instants", page.Records)
	}
}

// The list refuses a store that does not exist, one without revisions —
// which keeps no trash —, and a page it cannot read.
func TestTheTrashListRefusesWhatItCannotList(t *testing.T) {
	app := startBin(t, clock.NewManualClock(epoch))
	for _, bad := range []struct{ query, code string }{
		{"store=bin/store/nothing", kit.WireNotFound},
		{"store=", kit.WireNotFound},
		{"store=bin/store/scores", kit.WireInvalid},
		{"store=bin/store/passes&limit=0", kit.WireInvalid},
		{"store=bin/store/passes&limit=1001", kit.WireInvalid},
		{"store=bin/store/passes&limit=many", kit.WireInvalid},
		{"store=bin/store/passes&offset=-1", kit.WireInvalid},
	} {
		r := call(t, app, "GET /_kit/api/trash?"+bad.query, noBody)
		if r.status < 400 || r.status >= 500 || r.errorCode(t) != bad.code {
			t.Errorf("trash?%s answers %d %s, want %s", bad.query, r.status, r.body, bad.code)
		}
	}
}
