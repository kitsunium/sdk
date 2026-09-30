package kit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// A person's rights (ADR 0006 §5, §6): their records found under every
// identity, exported without their secrets, erased or deleted, a held
// record left — and nothing of an erased record left in the files.

// One person known under two identities is found in every store that
// keeps her records, and only her records: her export carries them, with
// what art. 15 asks beside them, and no secret member.
func TestAPersonIsFoundUnderEveryIdentity(t *testing.T) {
	app := startPrivacy(t)
	seed(t)
	data, dataErr := DeskExport.Ask(t.Context(), Identities{IDs: []string{"ann@x.dev", "u-ann"}})
	if dataErr != nil {
		t.Fatal(dataErr)
	}
	byStore, counts := recordsByStore(t, data)
	if want := map[string]int{"desk/store/reports": 1, "people/store/customers": 1, "people/store/invoices": 1}; !maps.Equal(counts, want) {
		t.Fatalf("ann's export: %v", byStore)
	}
	checkReportsLine(t, data)
	r := byStore["desk/store/reports"][0]
	if got := [3]any{r["email"], r["name"], r["health"]}; got != [3]any{"ann@x.dev", "Ann", "asthma"} {
		t.Errorf("her report, as she receives it: %v", r)
	}
	if _, ok := r["trackHash"]; ok {
		t.Errorf("an export carries a secret member: %v", r)
	}
	if _, ok := byStore["people/store/customers"][0]["password"]; ok {
		t.Errorf("an export carries a password: %v", byStore["people/store/customers"][0])
	}
	if raw := mustJSON(t, data); bytes.Contains(raw, []byte("bob")) {
		t.Errorf("ann's export carries bob's data: %s", raw)
	}
	exportJournaled(t, privacyOf(t, app), "ann")
}

// recordsByStore decodes an export's records, store by store, and counts
// them.
func recordsByStore(t *testing.T, data kit.PersonalData) (map[string][]map[string]any, map[string]int) {
	t.Helper()
	records, counts := map[string][]map[string]any{}, map[string]int{}
	for _, s := range data.Stores {
		for _, raw := range s.Records {
			var m map[string]any
			must(t, json.Unmarshal(raw, &m))
			records[s.Store] = append(records[s.Store], m)
			counts[s.Store]++
		}
	}
	return records, counts
}

// checkReportsLine checks the reports' part of an export says their
// purpose and their retention.
func checkReportsLine(t *testing.T, data kit.PersonalData) {
	t.Helper()
	for _, s := range data.Stores {
		if s.Store == "desk/store/reports" && (s.Purpose == "" || !strings.Contains(s.Retention, "90 days")) {
			t.Errorf("the reports' line lacks its purpose or retention: %+v", s)
		}
	}
}

// exportJournaled checks the journal names an export by reference only:
// never the person, whose identities contain who.
func exportJournaled(t *testing.T, priv model.Privacy, who string) {
	t.Helper()
	exports := 0
	for _, e := range priv.Journal {
		if e.Op != model.JournalExport {
			continue
		}
		exports++
		if e.Subject == "" || strings.Contains(e.Subject, who) {
			t.Errorf("an export's journal entry: %+v", e)
		}
	}
	if exports == 0 {
		t.Errorf("the export is not journaled: %+v", priv.Journal)
	}
	if raw := mustJSON(t, priv); bytes.Contains(raw, []byte(who+"@")) {
		t.Errorf("the journal names an identity: %s", raw)
	}
}

// An erasure clears the classified members and the subject, stamps the
// erased field and keeps the rest; a store that says DeleteOnErasure
// deletes; a held record is left and listed.
func TestAnErasureClearsDeletesAndHolds(t *testing.T) {
	app := startPrivacy(t)
	seed(t)
	done, doneErr := DeskErase.Dispatch(t.Context(), Identities{IDs: []string{"ann@x.dev", "u-ann"}})
	if doneErr != nil {
		t.Fatal(doneErr)
	}
	checkErasureParts(t, done)
	r, rErr := Reports.Get(t.Context(), "r1")
	if rErr != nil {
		t.Fatal(rErr)
	}
	checkErasedReport(t, r)
	if _, err := Customers.Get(t.Context(), "u-ann"); err == nil {
		t.Error("an account survived an erasure that deletes")
	}
	if i, err := Invoices.Get(t.Context(), "i1"); err != nil || i.Address == "" {
		t.Error("a held invoice was erased")
	}
	if b, err := Reports.Get(t.Context(), "r2"); err != nil || b.Email != "bob@x.dev" {
		t.Error("bob's report was erased with ann's")
	}
	// The journal names what was done, never an identity or a key.
	raw := string(mustJSON(t, privacyOf(t, app)))
	if leaked := mentions(raw, "ann@x.dev", "u-ann", `"r1"`, "i1"); len(leaked) > 0 {
		t.Errorf("the Privacy page names %v: %s", leaked, raw)
	}
}

// checkErasureParts checks what ann's erasure says it did: her report
// erased, her account deleted, her invoice held.
func checkErasureParts(t *testing.T, done kit.Erasure) {
	t.Helper()
	parts := map[string]model.StoreErasure{}
	for _, s := range done.Stores {
		parts[s.Store] = s
	}
	if got := parts["desk/store/reports"]; !slices.Equal(got.Erased, []string{"r1"}) {
		t.Errorf("reports: %+v", got)
	}
	if got := parts["people/store/customers"]; !slices.Equal(got.Deleted, []string{"u-ann"}) {
		t.Errorf("accounts: %+v", got)
	}
	if got := parts["people/store/invoices"]; !slices.Equal(got.Held, []string{"i1"}) {
		t.Errorf("invoices, held until their instant: %+v", got)
	}
}

// checkErasedReport checks an erased report keeps nothing personal, and
// keeps the rest: its category, what Anonymise generalised, its stamp.
func checkErasedReport(t *testing.T, r Report) {
	t.Helper()
	if left := r.Explanation + r.Email + r.Name + r.Health + r.TrackHash + r.Notes[0].Body; left != "" || r.Birth != nil {
		t.Errorf("an erased report keeps personal data: %+v", r)
	}
	if kept := fmt.Sprintf("%s %s %d %t", r.Category, r.Notes[0].Public, r.BirthYear, r.ErasedAt != nil); kept != "spam kept 1986 true" {
		t.Errorf("an erased report lost what it keeps (%s): %+v", kept, r)
	}
}

// A hold keeps a record from erasure and deletion — the product's own
// Delete answers a Conflict naming the hold, never its reason — and a
// release gives it back.
func TestAHoldStopsErasureAndDeletion(t *testing.T) {
	app := startPrivacy(t)
	seed(t)
	ctx := t.Context()
	must(t, Reports.Hold(ctx, "r1", "a court case about Ann"))
	refusedAsHeld(t, Reports.Delete(ctx, "r1"), "court")
	refusedAsHeld(t, Reports.Erase(ctx, "r1", "asked"), "court")
	done, err := DeskErase.Dispatch(ctx, Identities{IDs: []string{"ann@x.dev"}})
	if err != nil || len(done.Stores) != 1 || !slices.Equal(done.Stores[0].Held, []string{"r1"}) {
		t.Fatalf("a person's erasure over a held record: %+v, %v", done, err)
	}
	if data, err := DeskExport.Ask(ctx, Identities{IDs: []string{"ann@x.dev"}}); err != nil || len(data.Stores) != 1 {
		t.Fatalf("a held record is exported: %+v, %v", data, err)
	}
	if h := privacyOf(t, app).Holds; len(h) != 1 || h[0].Store != "desk/store/reports" || h[0].Record == "r1" {
		t.Errorf("holds: %+v", h)
	}
	must(t, Reports.Release(ctx, "r1"))
	if code, msg := failedWith(Reports.Release(ctx, "r1")); code != kit.WireNotFound {
		t.Errorf("a second release = %s %q", code, msg)
	}
	must(t, Reports.Delete(ctx, "r1"))
}

// refusedAsHeld checks err is the Conflict of a held record, and that it
// never says the hold's reason, of which secret is a word.
func refusedAsHeld(t *testing.T, err error, secret string) {
	t.Helper()
	if code, msg := failedWith(err); code != kit.WireConflict || strings.Contains(msg, secret) {
		t.Fatalf("a held record's refusal = %s %q", code, msg)
	}
}

// refusedNaming checks err is the Conflict of a held record that names
// what it should: HeldUntil's legal ground, the node that placed a hold.
func refusedNaming(t *testing.T, err error, named string) {
	t.Helper()
	if code, msg := failedWith(err); code != kit.WireConflict || !strings.Contains(msg, named) {
		t.Errorf("a held record's refusal = %s %q, want it to name %q", code, msg, named)
	}
}

// HeldUntil refuses a deletion before its instant, naming its ground; a
// person's hold holds every record they have now, and names who placed it:
// the endpoint that called, no user being signed in.
func TestHeldUntilAndAPersonsHold(t *testing.T) {
	startPrivacy(t)
	seed(t)
	ctx := t.Context()
	refusedNaming(t, Invoices.Delete(ctx, "i1"), "commercial code")
	if _, err := DeskHold.Dispatch(ctx, Identities{IDs: []string{"u-ann"}}); err != nil {
		t.Fatal(err)
	}
	refusedNaming(t, Customers.Delete(ctx, "u-ann"), "desk/command/hold")
}

// A key built from personal data cannot survive its erasure: the record is
// deleted instead, and the store says so.
func TestARecordKeyedByPersonalDataIsDeleted(t *testing.T) {
	startPrivacy(t)
	ctx := t.Context()
	must(t, Customers.Insert(ctx, Customer{ID: "u-cid", Email: "cid@x.dev"}))
	// Store.Erase clears — the subject, which is the key, included.
	if err := Customers.Erase(ctx, "u-cid", "asked"); err != nil {
		t.Fatal(err)
	}
	if _, err := Customers.Get(ctx, "u-cid"); err == nil {
		t.Error("a record whose key its erasure clears still exists")
	}
}

// The subject index is rebuilt from the documents at every start: a new
// index key finds the same records.
func TestALostIndexKeyRebuildsTheIndex(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	t.Setenv("KIT_INDEX_KEY", "the first index key, long enough")
	app := startPrivacyOn(t, kit.DataDir(dir))
	seed(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, app.Stop(ctx))
	t.Setenv("KIT_INDEX_KEY", "another index key, as long as it")
	must(t, app.Start(t.Context()))
	data, err := DeskExport.Ask(t.Context(), Identities{IDs: []string{"ann@x.dev", "u-ann"}})
	if err != nil || len(data.Stores) != 3 {
		t.Fatalf("after a new index key: %+v, %v", data, err)
	}
	t.Setenv("KIT_INDEX_KEY", "short")
	must(t, app.Stop(ctx))
	if err := app.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "internal error") && !strings.Contains(err.Error(), "KIT_INDEX_KEY") {
		t.Errorf("a short index key: %v", err)
	}
}

// The journal's chain verifies, and an entry changed on disk breaks it at
// its number. The erased bytes leave every file of the data directory.
func TestErasureLeavesNoTrace(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := startPrivacyOn(t, kit.DataDir(dir))
	ctx := t.Context()
	must(t, Reports.Insert(ctx, Report{ID: "r1", Email: "trace@x.dev", Name: "Tracy Trace", Explanation: "unforgettable words"}))
	must(t, Reports.Put(ctx, Report{ID: "r1", Email: "trace@x.dev", Name: "Tracy Trace", Explanation: "unforgettable words, again"}))
	if _, err := DeskErase.Dispatch(ctx, Identities{IDs: []string{"trace@x.dev"}}); err != nil {
		t.Fatal(err)
	}
	data, dataErr := os.OpenRoot(dir)
	if dataErr != nil {
		t.Fatal(dataErr)
	}
	defer func() {
		if err := data.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	for _, path := range filesHolding(t, data.FS(), "trace@x.dev", "Tracy Trace", "unforgettable") {
		t.Errorf("%s still holds erased data", path)
	}
	if c := privacyOf(t, app).Chain; c.BrokenAt != 0 || c.Entries == 0 {
		t.Fatalf("the chain: %+v", c)
	}
	ctxStop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(t, app.Stop(ctxStop))
	const journal = "kit.privacy/journal.json"
	raw, rawErr := data.ReadFile(journal)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	must(t, data.WriteFile(journal, bytes.Replace(raw, []byte(`"op": "erase"`), []byte(`"op": "export"`), 1), 0o600))
	must(t, app.Start(t.Context()))
	if c := privacyOf(t, app).Chain; c.BrokenAt != 1 {
		t.Errorf("a changed entry: %+v", c)
	}
}

// filesHolding lists the files of fsys, the secrets' aside, that hold any
// of the words.
func filesHolding(t *testing.T, fsys fs.FS, words ...string) []string {
	t.Helper()
	var out []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, ".secrets") {
			return err
		}
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		if len(mentions(string(raw), words...)) > 0 {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
