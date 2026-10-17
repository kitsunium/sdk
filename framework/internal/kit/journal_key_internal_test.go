package kit

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"

	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The privacy journal's data key (ADR 0178 §C3): one per calendar month of
// an entry's instant, in UTC, read on the app's clock; an entry sealed
// before under its own key keeps opening; a person's erasure never reaches
// a month's key.

// journalMonthEdge is the last millisecond of a month far from the wall
// clock: an entry that took its month from the wall clock lands elsewhere.
var journalMonthEdge = time.Date(2031, 1, 31, 23, 59, 59, 999_000_000, time.UTC)

// A journal key names its entry's month in UTC: the last instant of a month
// is that month, the next millisecond the next one, and an instant written
// in another zone is read in UTC.
func TestAJournalKeyIsTheMonthInUTC(t *testing.T) {
	paris := time.FixedZone("UTC+2", 2*60*60)
	for at, want := range map[time.Time]string{
		journalMonthEdge:                                                  "j:2031-01",
		journalMonthEdge.Add(time.Millisecond):                            "j:2031-02",
		time.Date(2026, 11, 1, 1, 0, 0, 0, paris):                         "j:2026-10",
		time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC):                  "j:2026-12",
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC):                       "j:2027-01",
		time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC):                     "j:2026-10",
		time.Date(2026, 3, 1, 0, 30, 0, 0, time.FixedZone("UTC+1", 3600)): "j:2026-02",
	} {
		if got := journalRef(at); got != want {
			t.Errorf("journalRef(%s) = %q, want %q", at.Format(time.RFC3339Nano), got, want)
		}
	}
}

// journalApp runs a store of cards on the data in dir, on clk.
func journalApp(t *testing.T, dir string, clk clock.Timed) (*App, *StoreService[card]) {
	t.Helper()
	pinDataKeyWithoutFileStore(t)
	svc := NewService("journalkeys", "Cards whose journal is sealed, for the journal key tests.")
	cards := svc.Store("cards", func(c card) string { return c.ID })
	app := NewApp("journalkeys", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard), Clock(clk))
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	return app, cards
}

// dataKeys counts the data keys kit's store of keys holds.
func dataKeys(t *testing.T, app *App) int {
	t.Helper()
	n, err := app.privacyKeyStore().engine().Count(t.Context())
	must(t, err)
	return n
}

// journalRefs are the data keys each journal entry's boxes name, as the
// entries rest, in the journal's order.
func journalRefs(t *testing.T, app *App) []string {
	t.Helper()
	_, j := app.privacyStores()
	all, err := app.allJournal(t.Context())
	must(t, err)
	out := make([]string, len(all))
	for i, e := range all {
		refs, err := j.sealing().refsAt(t.Context(), e.key())
		must(t, err)
		out[i] = strings.Join(refs, ",")
	}
	return out
}

// Entries with a reason — the ones that seal something — share their
// month's key: N of them file one key, not N. The month is the app's
// clock's, in UTC: the last millisecond of January and the first of
// February are two keys, and the wall clock, years away, is neither.
func TestJournalEntriesShareTheirMonthsKey(t *testing.T) {
	needsFileStore(t)
	clk := clock.NewManualClock(journalMonthEdge)
	app, cards := journalApp(t, t.TempDir(), clk)
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "n"}))
	recs, err := app.Records(cards.id)
	must(t, err)
	before := dataKeys(t, app)
	const entries int = 20
	for i := range entries {
		must(t, recs.EraseFields(ctx, "c1", "moderated, pass "+string(rune('a'+i)), "/note"))
	}
	for range entries {
		_, err := recs.Get(ctx, "c1")
		must(t, err)
	}
	if got := dataKeys(t, app) - before; got != 1 {
		t.Errorf("%d entries with a reason and %d reads filed %d data keys, want 1: the month's", entries, entries, got)
	}
	clk.Advance(time.Millisecond)
	must(t, recs.EraseFields(ctx, "c1", "moderated in February", "/note"))
	if got := dataKeys(t, app) - before; got != 2 {
		t.Errorf("after the month turned, %d journal keys, want 2", got)
	}
	refs := journalRefs(t, app)
	sealed := map[string]int{}
	for _, r := range refs {
		if r != "" {
			sealed[r]++
		}
	}
	if sealed["j:2031-01"] != entries || sealed["j:2031-02"] != 1 || len(sealed) != 2 {
		t.Errorf("the entries' boxes name %v, want %d under j:2031-01 and 1 under j:2031-02", sealed, entries)
	}
	all, err := app.allJournal(ctx)
	must(t, err)
	if last := all[len(all)-1]; last.Reason != "moderated in February" {
		t.Errorf("the last entry's reason reads %q", last.Reason)
	}
	if check := verifyChain(all); check.BrokenAt != 0 {
		t.Errorf("the chain breaks at %d", check.BrokenAt)
	}
}

// An entry sealed before the journal had its month's key — under a data key
// of its own, "r:" and its reference — still opens after: its box names its
// key, nothing rewrites it, and the chain still verifies across the two.
func TestAnEntrySealedUnderItsOwnKeyStillOpens(t *testing.T) {
	needsFileStore(t)
	clk := clock.NewManualClock(time.Date(2031, 3, 10, 9, 0, 0, 0, time.UTC))
	app, cards := journalApp(t, t.TempDir(), clk)
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "n"}))
	_, j := app.privacyStores()
	keys, err := app.referenceKeys(ctx)
	must(t, err)
	old := legacyEntry(t, app, j, keys, "written before the month's key")
	recs, err := app.Records(cards.id)
	must(t, err)
	must(t, recs.EraseFields(ctx, "c1", "written after", "/note"))
	refs := journalRefs(t, app)
	if len(refs) != 2 || refs[0] != keys.ownRef(j.id, old.key()) || refs[1] != "j:2031-03" {
		t.Fatalf("the entries' boxes name %q, want the old entry's own key then the month's", refs)
	}
	all, err := app.allJournal(ctx)
	must(t, err)
	if all[0].Reason != "written before the month's key" || all[1].Reason != "written after" {
		t.Errorf("the reasons read %q and %q", all[0].Reason, all[1].Reason)
	}
	if check := verifyChain(all); check.BrokenAt != 0 || check.Entries != 2 {
		t.Errorf("the chain across the two keys: %+v", check)
	}
}

// legacyEntry writes the journal's next entry as kit sealed it before ADR
// 0178: its reason under a data key of its own.
func legacyEntry(t *testing.T, app *App, j *StoreService[journalRecord], keys *referenceKeys, reason string) journalRecord {
	t.Helper()
	ctx := t.Context()
	app.privacy.chain.Lock()
	defer app.privacy.chain.Unlock()
	must(t, app.loadChain(ctx, j))
	e := journalRecord{Seq: app.privacy.seq + 1, At: app.clock.Now().UTC(), Op: "hold", By: "cli", Reason: reason}
	e.Hash = chainHash(app.privacy.last, e)
	raw, err := json.Marshal(e)
	must(t, err)
	se := j.sealing()
	sealed, err := se.seal(ctx, raw, e.key(), keys.ownRef(j.id, e.key()), nil)
	must(t, err)
	must(t, se.inner.Write(ctx, plug.DocumentValue{JSON: sealed, Value: e}, insertOnly))
	app.privacy.seq, app.privacy.last = e.Seq, e.Hash
	return e
}

// A person's erasure destroys their key and the keys their records were
// sealed under, never a month's journal key: the journal names them by a
// reference only, and its entries — the erasure's own among them — keep
// opening after it.
func TestAnErasureLeavesTheJournalsKey(t *testing.T) {
	needsFileStore(t)
	clk := clock.NewManualClock(time.Date(2031, 5, 20, 9, 0, 0, 0, time.UTC))
	app, cards := journalApp(t, t.TempDir(), clk)
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "ann's"}))
	must(t, cards.Insert(ctx, card{ID: "c2", Owner: "bob", Note: "bob's"}))
	recs, err := app.Records(cards.id)
	must(t, err)
	must(t, recs.EraseFields(ctx, "c1", "ann's note moderated", "/note"))
	must(t, recs.EraseFields(ctx, "c2", "bob's note moderated", "/note"))
	z, err := app.sealing(ctx)
	must(t, err)
	has := func(ref string) bool {
		_, ok, err := keyStore{app.privacyKeyStore()}.Get(ctx, ref)
		must(t, err)
		return ok
	}
	if !has("j:2031-05") || !has(z.refs.personRef("ann")) {
		t.Fatal("the month's key or ann's is missing before the erasure")
	}
	if _, err := app.erase(context.WithoutCancel(ctx), "ann asked", []string{"ann"}); err != nil {
		t.Fatal(err)
	}
	if has(z.refs.personRef("ann")) {
		t.Error("ann's key outlives her erasure")
	}
	if !has("j:2031-05") || !has(z.refs.personRef("bob")) {
		t.Error("ann's erasure destroyed the month's journal key or bob's")
	}
	all, err := app.allJournal(ctx)
	must(t, err)
	reasons := map[string]bool{}
	for _, e := range all {
		reasons[e.Reason] = true
		if strings.Contains(e.Subject+e.Record, "ann") {
			t.Errorf("an entry names ann: %+v", e)
		}
	}
	for _, want := range []string{"ann's note moderated", "bob's note moderated", "ann asked"} {
		if !reasons[want] {
			t.Errorf("after the erasure, no entry reads %q: %v", want, reasons)
		}
	}
}
