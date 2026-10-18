package sqlite_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// keyedCopy copies the fixture's data directory into a directory of the
// test's, its secret store's directory under the name the product keeps it.
func keyedCopy(t *testing.T, fixture string) string {
	t.Helper()
	dir := formerCopy(t, fixture)
	if err := os.Rename(filepath.Join(dir, "_secrets"), filepath.Join(dir, ".secrets")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// secretFiles are the files of the secret store in dir, by name, with their
// bytes.
func secretFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	entries, err := os.ReadDir(filepath.Join(dir, ".secrets", "store"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".secret" || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ".secrets", "store", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

// formerSecretFiles are the files of secretFiles a former release wrote.
func formerSecretFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for name, b := range secretFiles(t, dir) {
		if strings.HasPrefix(name, "kit-") {
			out[name] = b
		}
	}
	return out
}

// dropFormerSecrets removes from the secret store in dir what a former
// release kept: what the minor that ends ADR 0180's window does once nothing
// is left under it.
func dropFormerSecrets(t *testing.T, dir string) {
	t.Helper()
	for name := range formerSecretFiles(t, dir) {
		if err := os.Remove(filepath.Join(dir, ".secrets", "store", name)); err != nil {
			t.Fatal(err)
		}
	}
}

// secretValue is the current value of name in store, "" when it holds none.
func secretValue(t *testing.T, store secret.Store, name string) string {
	t.Helper()
	v, err := store.Get(t.Context(), name)
	if errors.Is(err, secret.NotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(v.Value.Reveal())
}

// closeStore releases a secret store a test opened.
func closeStore(store secret.Store) {
	if c, ok := store.(io.Closer); ok {
		_ = c.Close()
	}
}

// readsAsItWas starts the keyed product on dir and checks that it reads what
// the former release read: every case, version, former value and the trash,
// a person's data opened, the erased person erased.
func readsAsItWas(t *testing.T, dir, fixture string, onSQLite bool) *keyedAtelier {
	t.Helper()
	k := newKeyed(t, dir, onSQLite, false)
	run(t, k.app)
	want, err := os.ReadFile(filepath.Join(fixture, "dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := keyedDump(t, k); !bytes.Equal(got, want) {
		t.Errorf("what the product reads:\n got %s\nwant %s", got, want)
	}
	erased, err := k.export.Dispatch(t.Context(), atelierWho{ID: "cy@atelier.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range erased.Stores {
		if len(s.Records) > 0 {
			t.Errorf("the erased person's data opened again: %+v", s)
		}
	}
	return k
}

// What a former release kept under kit-data-key, kit-index-key and
// kit-smtp-url (testdata/former-keys, written before ADR 0180 step 5b) is
// read the first time this release meets it, as it was: the privacy
// journal's chain and the holds, the cases with their versions, former values
// and trash, a person's data opened, the erased person still erased, the
// letter and the notice about nobody still queued delivered — the notice
// sealed under kit-data-key itself, under the label kit/v1 sealed at rest,
// by its first version. The index key and the SMTP URL are copied to their
// present names, data-key is made under its own, every data key is
// re-wrapped under it, and nothing the former release kept is written: its
// three secrets stay byte for byte. A second start copies and moves nothing;
// and once the former secrets are gone — what the minor that ends the window
// does —, everything still reads.
func TestAFormerReleasesKeysAreReWrappedAndReadAsTheyWere(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-keys", kind)
			dir := keyedCopy(t, fixture)
			formerFiles := formerSecretFiles(t, dir)
			wantNames, err := os.ReadFile(filepath.Join(fixture, "names.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := keyedJSON(t, keyedNames(t, keyedSecrets(t, dir))); !bytes.Equal(got, wantNames) {
				t.Fatalf("the fixture's secrets:\n got %s\nwant %s", got, wantNames)
			}

			// The commands meet it first: they copy and re-wrap, then read
			// what the former release read.
			for name, args := range map[string][]string{
				"journal.txt": {"privacy", "journal", "-verify"},
				"holds.txt":   {"privacy", "holds"},
			} {
				status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, args...)
				want, err := os.ReadFile(filepath.Join(fixture, name))
				if err != nil {
					t.Fatal(err)
				}
				if status != 0 || !bytes.Equal(got, want) {
					t.Errorf("%v: %d\n got %s\nwant %s", args, status, got, want)
				}
			}
			store := keyedSecrets(t, dir)
			names := keyedNames(t, store)
			for _, name := range []string{"app-data-key", "app-index-key", "app-smtp-url"} {
				if !slices.Equal(names[name], []int{1}) {
					t.Errorf("%s: versions %v, want [1]", name, names[name])
				}
			}
			for _, name := range []string{"index-key", "smtp-url"} {
				if got, want := secretValue(t, store, "app-"+name), secretValue(t, store, "kit-"+name); got != want || got == "" {
					t.Errorf("app-%s holds %q, kit-%s %q", name, got, name, want)
				}
			}
			closeStore(store)
			if got := formerSecretFiles(t, dir); !maps.EqualFunc(got, formerFiles, bytes.Equal) {
				t.Errorf("what the former release kept changed: %v", slices.Sorted(maps.Keys(got)))
			}

			for round := range 2 {
				k := readsAsItWas(t, dir, fixture, onSQLite)
				if round == 0 {
					if got := received(t, "the letter kept in the queue", k.posted); got != (atelierLetter{To: "bob@atelier.test", Body: "a letter to bob, kept"}) {
						t.Errorf("the letter kept: %+v", got)
					}
					if got := received(t, "the notice kept in the queue", k.pinned); got != (keyedNotice{Text: "a notice about nobody, kept"}) {
						t.Errorf("the notice kept, sealed under kit-data-key itself: %+v", got)
					}
				}
				if err := k.app.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				if got := keyedNames(t, keyedSecrets(t, dir)); !maps.EqualFunc(got, names, slices.Equal) {
					t.Errorf("round %d: the secrets changed: %v, were %v", round, got, names)
				}
				if got := formerSecretFiles(t, dir); !maps.EqualFunc(got, formerFiles, bytes.Equal) {
					t.Errorf("round %d: what the former release kept changed", round)
				}
			}

			dropFormerSecrets(t, dir)
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A copy never replaces: an index key kept under both names with two values
// refuses the start with CodeRenameTarget, and nothing is copied, made or
// re-wrapped — every reference the framework keeps was made under one of the
// two, and which is the operator's to say.
func TestTheSecretsCopyNeverReplacesAnIndexKey(t *testing.T) {
	needsFileStore(t)
	dir := keyedCopy(t, filepath.Join("testdata", "former-keys", "files"))
	store := keyedSecrets(t, dir)
	if _, err := store.Put(t.Context(), "app-index-key", secret.FromString("another index key, of 32 bytes..")); err != nil {
		t.Fatal(err)
	}
	closeStore(store)
	files, data := secretFiles(t, dir), dataOf(t, dir)
	err := newKeyed(t, dir, false, false).app.Start(t.Context())
	if !errs.HasCode(err, app.CodeRenameTarget) {
		t.Fatalf("the start with two index keys: %v", err)
	}
	if got := secretFiles(t, dir); !maps.EqualFunc(got, files, bytes.Equal) {
		t.Errorf("the secrets changed: %v", slices.Sorted(maps.Keys(got)))
	}
	if got := dataOf(t, dir); !maps.EqualFunc(got, data, bytes.Equal) {
		t.Errorf("the data directory changed: %v", slices.Sorted(maps.Keys(got)))
	}
}

// dataOf is treeOf without the secret store's directory, which a start
// guards — its .gitignore, its locks — before it reads anything.
func dataOf(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	tree := treeOf(t, dir)
	maps.DeleteFunc(tree, func(name string, _ []byte) bool { return strings.HasPrefix(name, ".secrets/") })
	return tree
}

// A copy the store refuses half-way — the index key copied, the SMTP URL
// not — refuses the start with CodeRenameFailed and leaves what the former
// release kept as it was, readable; once the cause is gone, the next start
// copies the rest and reads everything as it was.
func TestASecretsCopyThatFailsHalfWayResumes(t *testing.T) {
	needsFileStore(t)
	fixture := filepath.Join("testdata", "former-keys", "files")
	dir := keyedCopy(t, fixture)
	formerFiles := formerSecretFiles(t, dir)
	// A directory where the store would take app-smtp-url's lock: its Put
	// fails, its Get finds nothing.
	sum := sha256.Sum256([]byte("kitsunium/secret/app-smtp-url"))
	blocked := filepath.Join(dir, ".secrets", "store", hex.EncodeToString(sum[:])+".lock")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	err := newKeyed(t, dir, false, false).app.Start(t.Context())
	if !errs.HasCode(err, app.CodeRenameFailed) {
		t.Fatalf("the start whose copy failed: %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	store := keyedSecrets(t, dir)
	names := keyedNames(t, store)
	closeStore(store)
	if _, copied := names["app-index-key"]; !copied {
		t.Errorf("the index key was not copied before the refusal: %v", names)
	}
	if _, made := names["app-data-key"]; made {
		t.Errorf("data-key was made by a start that was refused: %v", names)
	}
	if got := formerSecretFiles(t, dir); !maps.EqualFunc(got, formerFiles, bytes.Equal) {
		t.Errorf("what the former release kept changed")
	}
	k := readsAsItWas(t, dir, fixture, false)
	if got := received(t, "the notice kept in the queue", k.pinned); got.Text != "a notice about nobody, kept" {
		t.Errorf("the notice kept: %+v", got)
	}
	if got := secretValue(t, keyedSecrets(t, dir), "app-smtp-url"); got != "smtp://mail.atelier.test:25" {
		t.Errorf("app-smtp-url once resumed: %q", got)
	}
}

// After the move, data-key rotates as it did: a new version re-wraps every
// data key — the ones a former release made among them —, the first version
// can be pruned, and with the former secrets gone everything still reads.
func TestDataKeyRotatesOnceMoved(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-keys", kind)
			dir := keyedCopy(t, fixture)
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			store := keyedSecrets(t, dir)
			v, err := secret.Random(32)()
			if err == nil {
				_, err = store.Put(t.Context(), "app-data-key", v)
			}
			if err != nil {
				t.Fatal(err)
			}
			closeStore(store)
			// The start re-wraps under version 2; then version 1 goes.
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			store = keyedSecrets(t, dir)
			if err := store.Prune(t.Context(), "app-data-key", 1); err != nil {
				t.Fatal(err)
			}
			closeStore(store)
			dropFormerSecrets(t, dir)
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The way back (ADR 0180 §Breaking changes, step 5b): `privacy rewrap -back`
// moves every data key back under kit-data-key, which the former release
// opens. Shown without that release: with app-data-key gone, this release
// makes a new one, and reads everything as it was only because every data
// key is under kit-data-key again.
func TestTheWayBackMovesEveryDataKeyUnderTheFormerDataKey(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-keys", kind)
			dir := keyedCopy(t, fixture)
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			status, out := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "rewrap", "-back")
			if status != 0 || !strings.Contains(string(out), " data keys moved from the other data-key") || strings.HasPrefix(string(out), "0 ") {
				t.Fatalf("privacy rewrap -back: %d %s", status, out)
			}
			if err := os.Remove(filepath.Join(dir, ".secrets", "store", "app-data-key.secret")); err != nil {
				t.Fatal(err)
			}
			if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
