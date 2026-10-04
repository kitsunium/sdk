package kit_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// What a store's history owes ADR 0006, and what the Studio and the model
// show of it (ADR 0007 §4–§6): a hold keeps former values from pruning, an
// erasure takes them with what it clears, an export carries them; the
// Studio shows them redacted; the model says what a store remembers. And
// the password policy, once, on the SDK's own hashing.

// A held record's former values are not pruned while it is held; its first
// write after the release prunes them.
func TestAHeldRecordKeepsItsFormerValues(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	must(t, Holders.Hold(ctx, "h1", "litigation"))
	for i := 2; i <= 6; i++ {
		_, err := Holders.Update(ctx, "h1", emailOf(fmt.Sprintf("a%d@x.dev", i)))
		must(t, err)
	}
	if got := formerValues(t, "h1", "/email"); len(got) != 5 {
		t.Errorf("held: %q, want five", got)
	}
	must(t, Holders.Release(ctx, "h1"))
	_, err := Holders.Update(ctx, "h1", emailOf("a7@x.dev"))
	must(t, err)
	if got := formerValues(t, "h1", "/email"); !slices.Equal(got, []string{"a6@x.dev", "a5@x.dev", "a4@x.dev"}) {
		t.Errorf("released: %q", got)
	}
}

// A person's erasure takes the former values of what it clears with them —
// from the files too, once folded —, and keeps a public field's.
func TestAnErasureTakesTheFormerValues(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	startKeeper(t, kit.DataDir(dir))
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "erase-me-1@x.dev", Nick: "n1"}))
	must(t, Holders.Put(ctx, Holder{ID: "h1", Email: "erase-me-2@x.dev", Nick: "n2"}))
	if _, err := KeeperErase.Dispatch(ctx, Identities{IDs: []string{"erase-me-2@x.dev"}}); err != nil {
		t.Fatal(err)
	}
	if got := formerValues(t, "h1", "/email"); len(got) != 0 {
		t.Errorf("an erased address is remembered: %q", got)
	}
	if got := formerValues(t, "h1", "/nick"); !slices.Equal(got, []string{"n1"}) {
		t.Errorf("the public nickname's former values: %q", got)
	}
	data, err := os.OpenRoot(dir)
	must(t, err)
	defer func() {
		if err := data.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	for _, path := range filesHolding(t, data.FS(), "erase-me-1", "erase-me-2") {
		t.Errorf("%s still holds an erased address", path)
	}
}

// A person's export carries their records' former values — never a
// secret's.
func TestAnExportCarriesTheFormerValues(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev", Nick: "n1", Password: "$test$c2FsdA$aGFzaDE"}))
	must(t, Holders.Put(ctx, Holder{ID: "h1", Email: "a2@x.dev", Nick: "n2", Password: "$test$c2FsdA$aGFzaDI"}))
	data, err := KeeperExport.Ask(ctx, Identities{IDs: []string{"a2@x.dev"}})
	must(t, err)
	if len(data.Stores) != 1 || len(data.Stores[0].Former) != 1 {
		t.Fatalf("the export: %+v", data)
	}
	fields := data.Stores[0].Former[0].Fields
	if _, ok := fields["/password"]; ok || len(fields["/email"]) != 1 || len(fields["/nick"]) != 1 {
		t.Errorf("the former values exported: %+v", fields)
	}
	if text := string(mustJSON(t, data)); mentions(text, "aGFzaD") != nil || mentions(text, "a1@x.dev") == nil {
		t.Errorf("the export: %s", text)
	}
}

// shownAs is what a field's former values show, one per value: the value,
// "-" for none, and whether it says when it was replaced.
func shownAs(list []model.Former) []string {
	out := []string{}
	for _, f := range list {
		v := string(f.Value)
		if f.Value == nil {
			v = "-"
		}
		out = append(out, fmt.Sprintf("%s %t", v, !f.Until.IsZero()))
	}
	return out
}

// The Studio shows when each former value was replaced and by whom: a
// public value, never a personal one, and a secret's without its value.
func TestTheStudioShowsFormerValuesRedacted(t *testing.T) {
	app := startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev", Nick: "n1", Password: "$test$c2FsdA$aGFzaDE"}))
	must(t, Holders.Put(ctx, Holder{ID: "h1", Email: "a2@x.dev", Nick: "n2", Password: "$test$c2FsdA$aGFzaDI"}))
	r := call(t, app, "GET /_kit/api/former?store=keeper/store/holders&key=h1", noBody)
	var got model.RecordHistory
	r.json(t, &got)
	if leaked := mentions(string(r.body), "a1@x.dev", "aGFzaD"); leaked != nil {
		t.Errorf("the Studio shows %v: %s", leaked, r.body)
	}
	for pointer, want := range map[string][]string{
		"/nick":     {`"n1" true`},
		"/email":    {`"[redacted]" true`},
		"/password": {"- true"},
	} {
		if shown := shownAs(got.Fields[pointer]); !slices.Equal(shown, want) {
			t.Errorf("%s shows %q, want %q", pointer, shown, want)
		}
	}
	if r := call(t, app, "GET /_kit/api/former?store=keeper/store/holders&key=nobody", noBody); r.status != 404 {
		t.Errorf("a missing record: %d", r.status)
	}
}

// rememberedAs is what the model says a store remembers, in one line.
func rememberedAs(h *model.StoreHistory) string {
	if h == nil {
		return "nothing"
	}
	out := strings.Join(h.Fields, ",")
	for _, p := range h.Passwords {
		out += fmt.Sprintf(" | %s: %d characters, %d refused, declared in %s", p.Field, p.MinLength, p.NotReused, p.Source.File)
	}
	if h.Kept != nil && h.Bytes != nil {
		out += fmt.Sprintf(" | kept %d, weighs %t", *h.Kept, *h.Bytes > 0)
	}
	return out
}

// The model says what a store remembers — its fields, its policies, and on
// a running product what the histories weigh —, and the revision does not
// move with what they weigh.
func TestTheModelSaysWhatAStoreRemembers(t *testing.T) {
	app := startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	before := app.Graph()
	_, err := Holders.Update(ctx, "h1", emailOf("a2@x.dev"))
	must(t, err)
	after := app.Graph()
	got := rememberedAs(after.Node("keeper/store/holders").Store.History)
	want := "/email,/nick,/password | /password: 15 characters, 2 refused, declared in framework/internal/kit/history_test.go | kept 1, weighs true"
	if got != want {
		t.Errorf("the model says %q, want %q", got, want)
	}
	if !slices.Contains(after.Files(), model.File{File: "framework/internal/kit/history_test.go"}) {
		t.Error("the policy's file is not one the Studio may open")
	}
	if before.Revision != after.Revision {
		t.Errorf("the revision moved with the history's weight: %s → %s", before.Revision, after.Revision)
	}
}

// The policy on the SDK's own hashing, once: a password set verifies, and
// the current one is refused again. The SDK's Verify costs 144 ms by
// design, far more under the race detector: the rest of the policy's tests
// run on a fast hashing (passwords_internal_test.go).
func TestThePasswordPolicyOnTheSDKsHashing(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	secret := []byte("correct horse battery staple")
	must(t, HolderPasswords.Set(ctx, "h1", secret))
	if ok, err := HolderPasswords.Verify(ctx, "h1", secret); !ok || err != nil {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if err := HolderPasswords.Change(ctx, "h1", secret, secret); err == nil {
		t.Error("the current password was set again")
	}
}
