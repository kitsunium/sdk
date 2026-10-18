package mysql_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The secrets and the sealing label on MySQL (ADR 0180 step 5b): the
// framework's data keys, kept in app.Privacy's table on the database, wrapped
// under kit-data-key by a former release, are re-wrapped under app-data-key
// at the first start and read back as they were; the index key a former
// release kept as kit-index-key is copied to app-index-key. The former layout
// is made from what this release wrote, as an operator going back to the
// former release makes it: `privacy rewrap -back`, which the SQLite suite
// shows the former release's data needs, then the present secrets set
// aside.

// newKeyedAtelier is the atelier with its secrets kept in the file store
// beside its data, its data key and its index key made there.
func newKeyedAtelier(t *testing.T, raw, dir string, wait bool) *atelier {
	t.Helper()
	a := newAtelier(t, raw, dir, wait)
	for _, v := range []string{"ATELIER_SECRETS", "ATELIER_DATA_KEY", "ATELIER_INDEX_KEY"} {
		if err := os.Unsetenv(v); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// keptSecrets opens the file store beside the data in dir.
func keptSecrets(t *testing.T, dir string) secret.Store {
	t.Helper()
	key, err := secret.KeyFile(filepath.Join(dir, ".secrets", "key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := secret.NewFile(secret.FileConfig{Dir: filepath.Join(dir, ".secrets", "store"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, ok := store.(io.Closer); ok {
			_ = c.Close()
		}
	})
	return store
}

// writeKeyed writes what the keyed product keeps — versions, trash, an
// erased person, a held case, a letter kept in the queue — and returns what
// it reads of it.
func writeKeyed(t *testing.T, raw, dir string) []byte {
	t.Helper()
	ctx := t.Context()
	a := newKeyedAtelier(t, raw, dir, false)
	if err := a.app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("ann", a.cases.Insert(ctx, atelierCase{ID: "c-ann", Email: "ann@atelier.test", Note: "a first note about ann", Stage: 1}))
	must("ann 2", a.cases.Put(ctx, atelierCase{ID: "c-ann", Email: "ann@atelier.test", Note: "a second note about ann", Stage: 2}))
	must("bob", a.cases.Insert(ctx, atelierCase{ID: "c-bob", Email: "bob@atelier.test", Note: "a note about bob", Stage: 1}))
	must("bob trashed", a.cases.Delete(ctx, "c-bob"))
	must("cy", a.cases.Insert(ctx, atelierCase{ID: "c-cy", Email: "cy@atelier.test", Note: "a note about cy", Stage: 1}))
	_, err := a.forget.Dispatch(ctx, atelierWho{ID: "cy@atelier.test"})
	must("cy forgotten", err)
	must("dan", a.cases.Insert(ctx, atelierCase{ID: "c-dan", Email: "dan@atelier.test", Note: "a note about dan", Stage: 4}))
	must("dan held", a.cases.Hold(ctx, "c-dan", "a court's request"))
	dump := dumpAtelier(t, a)
	must("stop", a.app.Stop(ctx))
	w := newKeyedAtelier(t, raw, dir, true)
	must("start waiting", w.app.Start(ctx))
	must("letter kept", w.letters.Publish(ctx, atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}))
	must("stop waiting", w.app.Stop(ctx))
	return dump
}

// formerKeyed makes, from what this release wrote in dir, what a former
// release kept: every data key under kit-data-key, rotated once, the index
// key under kit-index-key, and nothing under the present names.
func formerKeyed(t *testing.T, raw, dir string) {
	t.Helper()
	ctx := t.Context()
	store := keptSecrets(t, dir)
	for range 2 {
		v, err := secret.Random(32)()
		if err == nil {
			_, err = store.Put(ctx, "kit-data-key", v)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	index, err := store.Get(ctx, "app-index-key")
	if err == nil {
		_, err = store.Put(ctx, "kit-index-key", index.Value)
	}
	if err != nil {
		t.Fatal(err)
	}
	if status := newKeyedAtelier(t, raw, dir, true).app.Main(ctx, []string{"privacy", "rewrap", "-back"}); status != 0 {
		t.Fatalf("privacy rewrap -back: %d", status)
	}
	for _, name := range []string{"app-data-key", "app-index-key"} {
		if err := os.Remove(filepath.Join(dir, ".secrets", "store", name+".secret")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAFormerReleasesKeysOnMySQLAreReWrappedAndReadAsTheyWere(t *testing.T) {
	raw := fresh(t)
	dir := t.TempDir()
	want := writeKeyed(t, raw, dir)
	formerKeyed(t, raw, dir)
	for round := range 2 {
		a := newKeyedAtelier(t, raw, dir, false)
		run(t, a.app)
		if round == 0 {
			if got := received(t, a.posted); got != (atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}) {
				t.Errorf("the letter kept: %+v", got)
			}
		}
		if got := dumpAtelier(t, a); string(got) != string(want) {
			t.Errorf("round %d, what the product reads:\n got %s\nwant %s", round, got, want)
		}
		if err := a.app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	store := keptSecrets(t, dir)
	names, err := store.Names(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "app-data-key") || !strings.Contains(got, "app-index-key") {
		t.Errorf("the secrets once moved: %s", got)
	}
	// Nothing depends on what the former release kept any more.
	for _, name := range []string{"kit-data-key", "kit-index-key"} {
		if err := os.Remove(filepath.Join(dir, ".secrets", "store", name+".secret")); err != nil {
			t.Fatal(err)
		}
	}
	a := newKeyedAtelier(t, raw, dir, false)
	run(t, a.app)
	if got := dumpAtelier(t, a); string(got) != string(want) {
		t.Errorf("without the former secrets, what the product reads:\n got %s\nwant %s", got, want)
	}
}
