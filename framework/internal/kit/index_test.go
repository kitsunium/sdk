package kit_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

func statusOf(err error) int {
	var ke *kit.Error
	if errors.As(err, &ke) {
		return ke.Status
	}
	return 0
}

func ids(accounts []Account) []string {
	out := make([]string, len(accounts))
	for i, a := range accounts {
		out[i] = a.ID
	}
	return out
}

// A unique index refuses a second holder of a key, follows every rewrite and
// delete, and a refused write leaves the store and the index untouched.
func TestUniqueIndex(t *testing.T) {
	start(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Accounts.Insert(ctx, Account{ID: "acc_1", Email: "ada@example.com", Name: "Ada"}))
	err := Accounts.Insert(ctx, Account{ID: "acc_2", Email: "ada@example.com", Name: "Impostor"})
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("a second holder of a unique key: %v", err)
	}
	if strings.Contains(err.Error(), "ada@example.com") {
		t.Errorf("the conflict quotes the index key: %v", err)
	}
	if _, err := Accounts.Get(ctx, "acc_2"); statusOf(err) != http.StatusNotFound {
		t.Fatalf("the refused insert was stored: %v", err)
	}
	got, err := Accounts.Lookup(ctx, "email", "ada@example.com")
	if err != nil || got.Name != "Ada" {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}

	// A rewrite moves the entity in the index.
	must(Accounts.Put(ctx, Account{ID: "acc_1", Email: "ada@lovelace.dev", Name: "Ada"}))
	if _, err := Accounts.Lookup(ctx, "email", "ada@example.com"); statusOf(err) != http.StatusNotFound {
		t.Fatalf("the old key still finds the entity: %v", err)
	}
	must(Accounts.Insert(ctx, Account{ID: "acc_2", Email: "ada@example.com", Name: "Second"}))

	// An Update taking a key another entity holds is refused, and changes
	// nothing.
	_, err = Accounts.Update(ctx, "acc_2", func(a *Account) error { a.Email = "ada@lovelace.dev"; a.Name = "Thief"; return nil })
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("an update into a held key: %v", err)
	}
	if a, err := Accounts.Get(ctx, "acc_2"); err != nil || a.Name != "Second" || a.Email != "ada@example.com" {
		t.Fatalf("the refused update changed the entity: %+v", a)
	}
	// Re-writing an entity with its own key is not a conflict.
	must(Accounts.Put(ctx, Account{ID: "acc_2", Email: "ada@example.com", Name: "Renamed"}))

	// A delete frees the key.
	must(Accounts.Delete(ctx, "acc_1"))
	if _, err := Accounts.Lookup(ctx, "email", "ada@lovelace.dev"); statusOf(err) != http.StatusNotFound {
		t.Fatalf("a deleted entity is still indexed: %v", err)
	}
	must(Accounts.Insert(ctx, Account{ID: "acc_3", Email: "ada@lovelace.dev"}))

	// Empty keys are not indexed: any number of entities may have none.
	must(Accounts.Insert(ctx, Account{ID: "acc_4"}))
	must(Accounts.Insert(ctx, Account{ID: "acc_5"}))
	if _, err := Accounts.Lookup(ctx, "email", ""); statusOf(err) != http.StatusNotFound {
		t.Errorf("the empty key found something: %v", err)
	}
}

func TestIndexFind(t *testing.T) {
	start(t)
	ctx := t.Context()
	for _, a := range []Account{
		{ID: "acc_c", Email: "c@x.dev", Teams: []string{"red", "blue"}},
		{ID: "acc_a", Email: "a@x.dev", Teams: []string{"red", "red"}},
		{ID: "acc_b", Email: "b@x.dev", Teams: []string{"blue"}},
	} {
		if err := Accounts.Put(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	red, err := Accounts.Find(ctx, "team", "red")
	if err != nil || !slices.Equal(ids(red), []string{"acc_a", "acc_c"}) {
		t.Fatalf("Find(red) = %v %v: ordered by store key, each entity once", ids(red), err)
	}
	if _, err := Accounts.Update(ctx, "acc_c", func(a *Account) error { a.Teams = []string{"blue"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if red, err := Accounts.Find(ctx, "team", "red"); err != nil || !slices.Equal(ids(red), []string{"acc_a"}) {
		t.Errorf("after an update, Find(red) = %v", ids(red))
	}
	none, err := Accounts.Find(ctx, "team", "green")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("an unknown key: %v %v, want an empty slice", none, err)
	}
	// Find reads a unique index too.
	if one, err := Accounts.Find(ctx, "email", "b@x.dev"); err != nil || !slices.Equal(ids(one), []string{"acc_b"}) {
		t.Errorf("Find on a unique index: %v %v", ids(one), err)
	}
	var ke *kit.Error
	if _, err := Accounts.Lookup(ctx, "team", "blue"); !errors.As(err, &ke) || ke.Code != kit.WireInvalid {
		t.Errorf("Lookup on a non-unique index: %v", err)
	}
	if _, err := Accounts.Find(ctx, "nope", "x"); !errors.As(err, &ke) || ke.Code != kit.WireInvalid {
		t.Errorf("Find on an unknown index: %v", err)
	}
	blue, err := Accounts.Filter(ctx, func(a Account) bool { return slices.Contains(a.Teams, "blue") })
	if err != nil || !slices.Equal(ids(blue), []string{"acc_b", "acc_c"}) {
		t.Errorf("Filter = %v %v", ids(blue), err)
	}
}

// Concurrent writers racing for one unique key: exactly one wins.
func TestUniqueIndexUnderContention(t *testing.T) {
	start(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := range 32 {
		wg.Go(func() {
			err := Accounts.Insert(context.Background(), Account{ID: kit.NewID("acc"), Email: "same@x.dev", Name: string(rune('a' + i%26))})
			mu.Lock()
			defer mu.Unlock()
			switch statusOf(err) {
			case 0:
				wins++
			case http.StatusConflict:
				conflicts++
			default:
				t.Errorf("unexpected %v", err)
			}
		})
	}
	wg.Wait()
	if wins != 1 || conflicts != 31 {
		t.Fatalf("%d writers won and %d conflicted, want 1 and 31", wins, conflicts)
	}
	if n, err := Accounts.Count(t.Context()); err != nil || n != 1 {
		t.Fatalf("the store holds %d accounts", n)
	}
}

// Every index read is a span on the store, named after the method, and the
// graph lists the indexes.
func TestIndexesInTheGraph(t *testing.T) {
	app := start(t)
	ctx := t.Context()
	if err := Accounts.Put(ctx, Account{ID: "acc_1", Email: "a@x.dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Accounts.Lookup(ctx, "email", "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := Accounts.Find(ctx, "team", "red"); err != nil {
		t.Fatal(err)
	}
	if _, err := Accounts.Filter(ctx, func(Account) bool { return true }); err != nil {
		t.Fatal(err)
	}
	r := call(t, app, "GET /_kit/api/traces?root=members/store/accounts", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	names := map[string]string{}
	for _, tr := range traces {
		for _, s := range tr.Spans {
			names[s.Name] = s.Op
		}
	}
	for _, name := range []string{"Lookup", "Find", "Filter"} {
		if names[name] != model.OpRead {
			t.Errorf("no read span named %s: %v", name, names)
		}
	}
	info := app.Graph().Node("members/store/accounts").Store
	want := []model.IndexInfo{{Name: "email", Unique: true}, {Name: "team"}}
	if !slices.Equal(info.Indexes, want) {
		t.Errorf("indexes %+v, want %+v", info.Indexes, want)
	}
}

// Indexes are rebuilt when a store loads its file; data that breaks a unique
// index refuses to start rather than answer lies.
func TestIndexesAreRebuiltOnLoad(t *testing.T) {
	t.Setenv("KIT_SMTP_URL", "")
	dir := t.TempDir()
	run := func(during func()) error {
		app := kit.NewApp("members", Members).With(kit.DataDir(dir), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.Logs(io.Discard))
		if err := app.Start(t.Context()); err != nil {
			return err
		}
		during()
		return app.Stop(context.Background())
	}
	if err := run(func() {
		if err := Accounts.Put(t.Context(), Account{ID: "acc_1", Email: "kept@x.dev", Teams: []string{"red"}}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(func() {
		if a, err := Accounts.Lookup(t.Context(), "email", "kept@x.dev"); err != nil || a.ID != "acc_1" {
			t.Fatalf("after a restart, Lookup = %+v %v", a, err)
		}
		if red, err := Accounts.Find(t.Context(), "team", "red"); err != nil || len(red) != 1 {
			t.Fatalf("after a restart, Find = %v", red)
		}
	}); err != nil {
		t.Fatal(err)
	}
	broken := `{"acc_1":{"id":"acc_1","email":"twice@x.dev"},"acc_2":{"id":"acc_2","email":"twice@x.dev"}}`
	if err := os.WriteFile(filepath.Join(dir, "members", "accounts.json"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(func() {})
	if !errs.HasCode(err, kit.CodeStoreIndex) {
		t.Fatalf("a store breaking its unique index started: %v", err)
	}
	if strings.Contains(errs.PublicOf(err), "twice@x.dev") {
		t.Errorf("the public text quotes the key: %q", errs.PublicOf(err))
	}
}

func TestIndexDeclarationProblems(t *testing.T) {
	svc := kit.NewService("bad-indexes", "")
	svc.Store("wrong-type", func(a Account) string { return a.ID },
		kit.Unique("name", func(i Item) string { return i.Name }))
	svc.Store("twice", func(a Account) string { return a.ID },
		kit.Unique("email", func(a Account) string { return a.Email }),
		kit.Index("email", func(a Account) []string { return nil }))
	svc.Store("nil-key", func(a Account) string { return a.ID },
		kit.Index[Account]("team", nil), kit.Unique[Account]("email", nil))
	svc.Store("bad-name", func(a Account) string { return a.ID },
		kit.Index("", func(a Account) []string { return nil }))
	err := kit.NewApp("bad", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v", err)
	}
	for _, want := range []string{
		"its index \"name\" reads kit_test.Item",
		"declares the index \"email\" twice",
		"the index \"team\" has a nil key function",
		"the index \"email\" has a nil key function",
		"index name \"\" must start with a letter",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report lacks %q:\n%s", want, err)
		}
	}
}
