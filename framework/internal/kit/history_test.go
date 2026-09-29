package kit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The history product (ADR 0007): accounts whose e-mail keeps its three
// former values, whose nickname keeps its ten, and whose password is a
// policy's. Its declarations are package-level, as a product writes them.

var Keeper = kit.NewService("keeper", "Accounts that remember what they held, for the history tests (ADR 0007).")

// Holder is an account.
type Holder struct {
	ID       string `json:"id"`
	Email    string `json:"email" kit:"subject,history=3"`
	Nick     string `json:"nick,omitempty" kit:"public,history=10"`
	Logins   int    `json:"logins"`
	Password string `json:"password,omitempty" kit:"secret"`
}

func (h Holder) Key() string { return h.ID }

var Holders = Keeper.Store("holders", Holder.Key,
	kit.Unique("email", func(h Holder) string { return h.Email }),
	kit.Purpose("Sign the holders in"))

// HolderPasswords refuses the last two passwords again.
var HolderPasswords = Holders.Passwords(func(h *Holder) *string { return &h.Password }, kit.NotReused(2))

var (
	KeeperExport = Keeper.Endpoint("POST /export", func(ctx context.Context, in Identities) (kit.PersonalData, error) {
		return kit.Export(ctx, in.IDs...)
	}, kit.Private())
	KeeperErase = Keeper.Endpoint("POST /erase", func(ctx context.Context, in Identities) (kit.Erasure, error) {
		return kit.Erase(ctx, "erasure requested by the person", in.IDs...)
	}, kit.Private())
)

// startKeeper runs the history product in dev, in memory unless opts give a
// data directory.
func startKeeper(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	app := kit.NewApp("keeper", Keeper).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	run(t, app)
	return app
}

// emailOf sets an account's e-mail.
func emailOf(email string) func(*Holder) error {
	return func(h *Holder) error { h.Email = email; return nil }
}

// formerValues are the values of a field's former values, as strings.
func formerValues(t *testing.T, key, pointer string) []string {
	t.Helper()
	former, err := Holders.Former(t.Context(), key, pointer)
	if err != nil {
		t.Fatalf("former %s of %s: %v", pointer, key, err)
	}
	out := []string{}
	for _, f := range former {
		var s string
		if err := json.Unmarshal(f.Value, &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// Four changes keep three former values, newest first, each with when it
// was replaced and by whom.
func TestAFieldKeepsItsFormerValues(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	startKeeper(t, kit.InMemory(), kit.Clock(clk))
	ctx := kit.WithUser(t.Context(), "admin", Who{})
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	for _, email := range []string{"a2@x.dev", "a3@x.dev", "a4@x.dev", "a5@x.dev"} {
		clk.Advance(time.Hour)
		_, err := Holders.Update(ctx, "h1", emailOf(email))
		must(t, err)
	}
	former, err := Holders.Former(ctx, "h1", "/email")
	must(t, err)
	var got []string
	for _, f := range former {
		got = append(got, fmt.Sprintf("%s %s %s", f.Value, f.Until.Sub(epoch), f.By))
	}
	want := []string{`"a4@x.dev" 4h0m0s admin`, `"a3@x.dev" 3h0m0s admin`, `"a2@x.dev" 2h0m0s admin`}
	if !slices.Equal(got, want) {
		t.Errorf("former values %q, want %q", got, want)
	}
	if _, err := Holders.Former(ctx, "h1", "/logins"); err == nil {
		t.Error("a field that keeps no former values answers")
	}
	if _, err := Holders.Former(ctx, "nobody", "/email"); err == nil {
		t.Error("a missing record answers")
	}
}

// A write that leaves a field as it was records nothing: a login counter
// written at every attempt pushes nothing out.
func TestAnUnchangedFieldRecordsNothing(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev", Nick: "n1"}))
	for range 20 {
		_, err := Holders.Update(ctx, "h1", func(h *Holder) error { h.Logins++; return nil })
		must(t, err)
	}
	must(t, Holders.Put(ctx, Holder{ID: "h1", Email: "a1@x.dev", Nick: "n1", Logins: 99}))
	for _, pointer := range []string{"/email", "/nick", "/password"} {
		if got := formerValues(t, "h1", pointer); len(got) != 0 {
			t.Errorf("%s remembers %q", pointer, got)
		}
	}
}

// A Put whose key is new inserts; several at once all stand, and the field
// remembers every value but the last.
func TestConcurrentPutsOfANewKey(t *testing.T) {
	startKeeper(t, kit.InMemory())
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			errs[i] = Holders.Put(t.Context(), Holder{ID: "race", Email: fmt.Sprintf("r%d@x.dev", i), Nick: fmt.Sprintf("n%d", i)})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("put %d: %v", i, err)
		}
	}
	if got := formerValues(t, "race", "/nick"); len(got) != 7 || len(slices.Compact(slices.Sorted(slices.Values(got)))) != 7 {
		t.Errorf("the nickname remembers %q", got)
	}
}

// A write the store refuses — a unique index — leaves the history as it was.
func TestARefusedWriteLeavesTheHistory(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	must(t, Holders.Insert(ctx, Holder{ID: "h2", Email: "b1@x.dev"}))
	if _, err := Holders.Update(ctx, "h1", emailOf("b1@x.dev")); err == nil {
		t.Fatal("the unique index let a taken address through")
	}
	if got := formerValues(t, "h1", "/email"); len(got) != 0 {
		t.Errorf("a refused write left %q", got)
	}
	_, err := Holders.Update(ctx, "h1", emailOf("a2@x.dev"))
	must(t, err)
	if got := formerValues(t, "h1", "/email"); !slices.Equal(got, []string{"a1@x.dev"}) {
		t.Errorf("former values %q", got)
	}
}

// historyFile edits the store's history as it lies in dir, the app stopped.
func historyFile(t *testing.T, dir string, edit func(map[string]json.RawMessage)) {
	t.Helper()
	data, err := os.OpenRoot(dir)
	must(t, err)
	defer func() {
		if err := data.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	const file = "keeper/holders.history.json"
	raw, err := data.ReadFile(file)
	must(t, err)
	docs := map[string]json.RawMessage{}
	must(t, json.Unmarshal(raw, &docs))
	edit(docs)
	must(t, data.WriteFile(file, mustJSON(t, docs), 0o600))
}

// A deleted record takes its history with it: a record inserted again
// under its key starts with none.
func TestADeletedRecordTakesItsHistory(t *testing.T) {
	startKeeper(t, kit.InMemory())
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "gone", Email: "g1@x.dev"}))
	_, err := Holders.Update(ctx, "gone", emailOf("g2@x.dev"))
	must(t, err)
	must(t, Holders.Delete(ctx, "gone"))
	must(t, Holders.Insert(ctx, Holder{ID: "gone", Email: "g3@x.dev"}))
	if got := formerValues(t, "gone", "/email"); len(got) != 0 {
		t.Errorf("a new record inherits %q", got)
	}
}

// crashed leaves in dir what two crashes would: a history whose record is
// gone — between a record's deletion and its history's —, and h1's current
// address at the head of its history — between a history's write and its
// record's.
func crashed(t *testing.T, dir string) {
	t.Helper()
	historyFile(t, dir, func(docs map[string]json.RawMessage) {
		docs["ghost"] = json.RawMessage(`{"key":"ghost","fields":{"/email":[{"value":"ghost@x.dev","until":"2026-09-27T09:00:00Z"}]}}`)
		var h1 struct {
			Key    string                       `json:"key"`
			Fields map[string][]json.RawMessage `json:"fields"`
		}
		must(t, json.Unmarshal(docs["h1"], &h1))
		head := json.RawMessage(`{"value":"a2@x.dev","until":"2026-09-27T10:00:00Z"}`)
		h1.Fields["/email"] = append([]json.RawMessage{head}, h1.Fields["/email"]...)
		docs["h1"] = mustJSON(t, h1)
	})
}

// A crash between a history's write and its record's leaves the current
// value at the head of the history — a duplicate, which kit reads past and
// the next change replaces —, never a former value lost; a history whose
// record is gone is removed at the next start.
func TestACrashLeavesADuplicateNeverAGap(t *testing.T) {
	dir := t.TempDir()
	app := startKeeper(t, kit.DataDir(dir))
	ctx := t.Context()
	must(t, Holders.Insert(ctx, Holder{ID: "h1", Email: "a1@x.dev"}))
	_, err := Holders.Update(ctx, "h1", emailOf("a2@x.dev"))
	must(t, err)
	must(t, app.Stop(context.Background()))
	crashed(t, dir)
	must(t, app.Start(t.Context()))
	if got := formerValues(t, "h1", "/email"); !slices.Equal(got, []string{"a1@x.dev"}) {
		t.Errorf("after a crash: %q, want the duplicate read past", got)
	}
	_, err = Holders.Update(ctx, "h1", emailOf("a3@x.dev"))
	must(t, err)
	if got := formerValues(t, "h1", "/email"); !slices.Equal(got, []string{"a2@x.dev", "a1@x.dev"}) {
		t.Errorf("the next change: %q, want no duplicate and no gap", got)
	}
	must(t, app.Stop(context.Background()))
	data, err := os.OpenRoot(dir)
	must(t, err)
	defer func() {
		if err := data.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	if held := filesHolding(t, data.FS(), "ghost@x.dev"); len(held) != 0 {
		t.Errorf("the orphan history is still in %v", held)
	}
}
