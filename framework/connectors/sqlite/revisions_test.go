package sqlite_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
)

// A record's revisions on a SQLite file (ADR 0007 §3, §4): the versions are
// rows of <table>___vs, which kit's migrations create, written in the
// record's own transaction; they rest sealed as the record does, a hold kit
// keeps on the same file keeps them from pruning — read in the write's
// transaction —, an erasure clears them, a rollback leaves them as they
// were, and they read back after a restart.

// editor is a person's page: their name sealed where it rests.
type editor struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Name  string `json:"name" kit:"personal"`
	Bio   string `json:"bio,omitempty"`
}

// errUndone rolls a transaction back.
var errUndone = errors.New("the transaction is undone")

func TestRevisionsOnTheFile(t *testing.T) {
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("KIT_INDEX_KEY", "an index key of more than 16 bytes")
	dir := t.TempDir()
	svc := app.NewService("wiki", "Editors' pages that keep their revisions, sealed; app.Privacy on SQLite.")
	editors := svc.Store("editors", func(e editor) string { return e.ID }, app.Revisions(2), app.Purpose("Show the editors' pages"))
	a := app.NewApp("wiki", svc).With(app.Database("main", sqlite.Engine(), app.Keeps(app.Privacy, editors)),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard))
	run(t, a)
	if st := a.Graph().Node("wiki/store/editors").Store; st.Backend != "sqlite" || st.History == nil || st.History.Revisions != 2 {
		t.Fatalf("the store: %+v", st)
	}
	ctx := app.WithUser(t.Context(), "u1", struct{}{})
	names := func(key string) []string {
		t.Helper()
		revs, err := editors.Revisions(ctx, key)
		if err != nil {
			t.Fatalf("the versions of %s: %v", key, err)
		}
		out := []string{}
		for _, r := range revs {
			out = append(out, strconv.FormatUint(r.Number, 10)+":"+r.Value.Name)
		}
		return out
	}
	rename := func(key, name string) {
		t.Helper()
		if _, err := editors.Update(ctx, key, func(e *editor) error { e.Name = name; return nil }); err != nil {
			t.Fatal(err)
		}
	}

	if err := editors.Insert(ctx, editor{ID: "e1", Email: "ann@wiki.test", Name: "Annabel Quist", Bio: "bio-one"}); err != nil {
		t.Fatal(err)
	}
	rename("e1", "Annabel Vesper")
	rename("e1", "Annabel Lowmoor")
	rename("e1", "Annabel Fenwick")
	if got := names("e1"); !slices.Equal(got, []string{"4:Annabel Fenwick", "3:Annabel Lowmoor", "2:Annabel Vesper"}) {
		t.Errorf("versions %q", got)
	}
	db := open(t, filepath.Join(dir, "main.sqlite"))
	rows, err := db.QueryContext(ctx, `SELECT doc FROM "wiki__editors___vs" WHERE doc_key = ? AND doc IS NOT NULL`, []byte("e1"))
	if err != nil {
		t.Fatal(err)
	}
	formers, weighs := 0, int64(0)
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			t.Fatal(err)
		}
		formers++
		weighs += int64(len(doc))
		if !bytes.Contains(doc, []byte(`"sealed:v1:`)) || bytes.Contains(doc, []byte("Annabel")) || bytes.Contains(doc, []byte("ann@wiki.test")) {
			t.Errorf("a version rests in clear: %s", doc)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || formers != 2 {
		t.Errorf("the former versions' rows: %d %v", formers, err)
	}
	// The model says what they weigh, sealed as they rest, from one
	// aggregate statement on the file (ADR 0007 §5).
	if h := a.Graph().Node("wiki/store/editors").Store.History; h.Versions == nil || *h.Versions != 2 ||
		h.Trashed == nil || *h.Trashed != 0 || h.VersionBytes == nil || *h.VersionBytes != weighs {
		t.Errorf("the versions weigh %v, %v, %v; want 2 former versions of %d bytes", h.Versions, h.Trashed, h.VersionBytes, weighs)
	}

	// A rollback leaves the versions as they were: they are rows of the
	// transaction.
	err = app.Transact(ctx, func(ctx context.Context) error {
		if _, err := editors.Update(ctx, "e1", func(e *editor) error { e.Name = "never"; return nil }); err != nil {
			return err
		}
		return errUndone
	})
	if !errors.Is(err, errUndone) {
		t.Fatal(err)
	}
	if got := names("e1"); !slices.Equal(got, []string{"4:Annabel Fenwick", "3:Annabel Lowmoor", "2:Annabel Vesper"}) {
		t.Errorf("versions %q after a rollback", got)
	}

	// A hold, kept on the same file, keeps them from pruning.
	if err := editors.Hold(ctx, "e1", "a court's order"); err != nil {
		t.Fatal(err)
	}
	rename("e1", "Annabel Hale")
	rename("e1", "Annabel Moss")
	if got := names("e1"); len(got) != 5 {
		t.Errorf("a held record's versions: %q, want every one since the hold", got)
	}
	if err := editors.Release(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	rename("e1", "Annabel Reed")
	if got := names("e1"); !slices.Equal(got, []string{"7:Annabel Reed", "6:Annabel Moss", "5:Annabel Hale"}) {
		t.Errorf("versions %q after the release's first write", got)
	}

	// An erasure clears them as it clears the record.
	if err := editors.Erase(ctx, "e1", "asked by the person"); err != nil {
		t.Fatal(err)
	}
	revs, err := editors.Revisions(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range revs {
		if r.Value.Name != "" || r.Value.Email != "" {
			t.Errorf("version %d after the erasure: %+v", r.Number, r.Value)
		}
	}
	if len(revs) != 3 || revs[1].Value.Bio != "bio-one" {
		t.Errorf("the erasure keeps what it does not clear: %+v", revs)
	}

	// They read back after a restart.
	if err := editors.Insert(ctx, editor{ID: "e2", Email: "bob@wiki.test", Name: "Robert Vale"}); err != nil {
		t.Fatal(err)
	}
	rename("e2", "Robert Hale")
	stopped, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(stopped); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := names("e2"); !slices.Equal(got, []string{"2:Robert Hale", "1:Robert Vale"}) {
		t.Errorf("versions %q after a restart", got)
	}
	back, err := editors.Restore(ctx, "e2", 1)
	if err != nil || back.Name != "Robert Vale" {
		t.Errorf("a restore after a restart: %+v %v", back, err)
	}
}
