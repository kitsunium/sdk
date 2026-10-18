package sqlite_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// danNote is what the fixture keeps of dan — no hold, no trash — that only
// his data key opens.
const danNote = "a note about dan"

// copyTree copies the tree src into dst, with the modes a data directory and
// its secret store need, but what skip names by its slash path.
func copyTree(t *testing.T, src, dst string, skip func(string) bool) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if skip != nil && rel != "." && skip(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// treeCopy copies the data directory dir into a directory of the test's.
func treeCopy(t *testing.T, dir string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "data")
	copyTree(t, dir, to, nil)
	return to
}

// restoredBeside is a copy of dir with what the backup pre holds put back,
// but the secret store and the store of data keys: the product's data as it
// was before the erasure — every box of dan in it —, beside the secrets and
// the data keys as they are now.
func restoredBeside(t *testing.T, pre, dir string) string {
	t.Helper()
	to := treeCopy(t, dir)
	copyTree(t, pre, to, func(rel string) bool {
		return strings.HasPrefix(rel, ".secrets") || strings.HasPrefix(rel, "app.privacy/keys")
	})
	return to
}

// danIsUnreadable starts the keyed product on dir and checks that nothing of
// dan opens: the start refuses data keys neither data-key opens, or his
// case and his export come back without what only his key opened.
func danIsUnreadable(t *testing.T, dir string, onSQLite bool, under string) {
	t.Helper()
	k := newKeyed(t, dir, onSQLite, false)
	if err := k.app.Start(t.Context()); err != nil {
		if !errs.HasCode(err, app.CodeSealOpen) {
			t.Fatalf("%s: the start: %v", under, err)
		}
		return
	}
	defer func() {
		if err := k.app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}()
	if c, err := k.cases.Get(t.Context(), "c-dan"); err == nil && c.Note == danNote {
		t.Errorf("%s: dan's case opened: %+v", under, c)
	}
	data, err := k.export.Dispatch(t.Context(), atelierWho{ID: "dan@atelier.test"})
	if err != nil {
		return
	}
	for _, s := range data.Stores {
		for _, r := range s.Records {
			if strings.Contains(string(r), danNote) {
				t.Errorf("%s: dan's export holds %+v", under, r)
			}
		}
	}
}

// A person erased once this release moved the data keys is unreadable at
// once under both roots (ADR 0180 step 5b, ADR 0142): the erasure destroys
// the one wrapped key he had — there is no other in the store of data keys,
// under either data-key — so every box of his, wherever it was copied,
// stops opening. Shown on his boxes as a backup of the data taken just
// before the erasure keeps them, restored beside the keys as the erasure
// left them: nothing of him opens with both data-keys, with kit-data-key
// alone, with app-data-key alone, or once `privacy rewrap -back` moved every
// data key back under kit-data-key. With his key not destroyed, his case
// opens.
func TestAPersonErasedAfterTheMoveIsUnreadableUnderEitherDataKey(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-keys", kind)
			dir := keyedCopy(t, fixture)
			k := readsAsItWas(t, dir, fixture, onSQLite)
			if c, err := k.cases.Get(t.Context(), "c-dan"); err != nil || c.Note != danNote {
				t.Fatalf("dan before the erasure: %+v %v", c, err)
			}
			if err := k.app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			pre := treeCopy(t, dir)
			k = newKeyed(t, dir, onSQLite, false)
			run(t, k.app)
			erased, err := k.forget.Dispatch(t.Context(), atelierWho{ID: "dan@atelier.test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := k.app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(erased.Stores) == 0 {
				t.Fatalf("the erasure erased nothing: %+v", erased)
			}

			danIsUnreadable(t, restoredBeside(t, pre, dir), onSQLite, "both data-keys")

			present := restoredBeside(t, pre, dir)
			if err := os.Remove(filepath.Join(present, ".secrets", "store", "app-data-key.secret")); err != nil {
				t.Fatal(err)
			}
			danIsUnreadable(t, present, onSQLite, "kit-data-key alone")

			former := restoredBeside(t, pre, dir)
			dropFormerSecrets(t, former)
			danIsUnreadable(t, former, onSQLite, "app-data-key alone")

			back := restoredBeside(t, pre, dir)
			if status, out := mainOf(t, newKeyed(t, back, onSQLite, false).atelier, "privacy", "rewrap", "-back"); status != 0 {
				t.Fatalf("privacy rewrap -back: %d %s", status, out)
			}
			if err := os.Remove(filepath.Join(back, ".secrets", "store", "app-data-key.secret")); err != nil {
				t.Fatal(err)
			}
			danIsUnreadable(t, back, onSQLite, "rewrap -back")
		})
	}
}
