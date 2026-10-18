package sqlite_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The product testdata/former-keys was written by (ADR 0180 step 5b): the
// release before the secrets and the sealing labels changed kept it, its
// secrets in the file store beside the data — kit-data-key, rotated once,
// kit-index-key and kit-smtp-url —, its data keys wrapped under
// kit-data-key, and a notice about nobody sealed under kit-data-key itself
// with the label kit/v1 sealed at rest. The declarations below are the ones
// that wrote it; a test that opens the fixture declares the same product.

// keyedNotice is a notice about nobody: no subject, no user, so its
// personal member is sealed under data-key itself where it rests.
type keyedNotice struct {
	Text string `json:"text" app:"personal"`
}

// keyedAtelier is the atelier of former_test.go with its secrets kept in
// the file store beside its data, and a topic of notices about nobody.
type keyedAtelier struct {
	*atelier
	notices *app.Topic[keyedNotice]
	// pinned are the notices delivered, opened; with wait set, the
	// subscriber holds every notice until its context ends, and none is.
	pinned chan keyedNotice
}

// keyedEnv unsets what the product reads from its environment, so that its
// secrets are kept in the file store beside its data and its data key and
// its index key are made there.
func keyedEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"ATELIER_SECRETS", "ATELIER_DATA_KEY", "ATELIER_INDEX_KEY", "ATELIER_SMTP_URL", "ATELIER_SECRETS_KEY"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatal(err)
		}
	}
}

// newKeyed declares the keyed product with its data in dir: on the SQLite
// database main when onSQLite, in the data directory otherwise.
func newKeyed(t *testing.T, dir string, onSQLite, wait bool) *keyedAtelier {
	t.Helper()
	svc := app.NewService("atelier", "Cases and letters about people, sealed where they rest.")
	k := &keyedAtelier{atelier: &atelier{posted: make(chan atelierLetter, 8)}, pinned: make(chan keyedNotice, 8)}
	a := k.atelier
	a.cases = svc.Store("cases", func(c atelierCase) string { return c.ID }, app.Purpose("Handle the cases"), app.Revisions(3))
	a.tally = svc.Store("tally", func(r tallyRow) string { return r.ID })
	a.letters = svc.Topic[atelierLetter]("letters")
	k.notices = svc.Topic[keyedNotice]("notices")
	svc.Subscribe("post", a.letters, func(ctx context.Context, l atelierLetter) error {
		if wait {
			<-ctx.Done()
			return ctx.Err()
		}
		a.posted <- l
		return nil
	})
	svc.Subscribe("pin", k.notices, func(ctx context.Context, n keyedNotice) error {
		if wait {
			<-ctx.Done()
			return ctx.Err()
		}
		k.pinned <- n
		return nil
	})
	a.export = svc.Command("export", func(ctx context.Context, w atelierWho) (app.PersonalData, error) {
		return app.Export(ctx, w.ID)
	})
	a.forget = svc.Command("forget", func(ctx context.Context, w atelierWho) (app.Erasure, error) {
		return app.Erase(ctx, "the person asked", w.ID)
	})
	keyedEnv(t)
	opts := []app.AppOption{app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard)}
	if onSQLite {
		opts = append(opts, app.Database("main", sqlite.Engine(), app.Keeps(app.Privacy, svc)))
	}
	a.app = app.NewApp("atelier", svc).With(opts...)
	return k
}

// keyedSecrets opens the file store beside the data in dir, as the product
// keeps it: what a test reads the secrets' names and versions from.
func keyedSecrets(t *testing.T, dir string) secret.Store {
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

// keyedDump is what the keyed product reads of what it keeps: the atelier's
// dump.
func keyedDump(t *testing.T, k *keyedAtelier) []byte {
	t.Helper()
	return dumpAtelier(t, k.atelier)
}

// keyedNames are the names the file store keeps, with each one's versions.
func keyedNames(t *testing.T, store secret.Store) map[string][]int {
	t.Helper()
	names, err := store.Names(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]int{}
	for _, name := range names {
		versions, err := store.Versions(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			out[name] = append(out[name], v.Version)
		}
	}
	return out
}

// keyedJSON is v as indented JSON, with a final newline.
func keyedJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}
