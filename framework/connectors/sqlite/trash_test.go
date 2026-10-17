package sqlite_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
)

// The trash on a SQLite file (ADR 0178 C4): a deletion by the product keeps
// the record as the row numbered 0 of <table>___vs, sealed as the record
// rested, and its versions beside it; a person's erasure purges theirs from
// the trash at once — no row of them is left — and leaves another person's,
// which comes back as it was. The trash's own contract on SQLite — the purge
// 30 days after on the app's clock, a store without revisions — is the
// conformance suite's (app/storetest, "trash", TestStoresConform).

// erasing names the people an erasure is for.
type erasing struct {
	IDs []string `json:"ids"`
}

func TestAnErasureEmptiesTheTrashOnTheFile(t *testing.T) {
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("KIT_INDEX_KEY", "an index key of more than 16 bytes")
	dir := t.TempDir()
	svc := app.NewService("wiki", "Editors' pages that keep their revisions and a trash, sealed; app.Privacy on SQLite.")
	editors := svc.Store("editors", func(e editor) string { return e.ID }, app.Revisions(2), app.Purpose("Show the editors' pages"))
	erase := svc.Command("erase", func(ctx context.Context, in erasing) (app.Erasure, error) {
		return app.Erase(ctx, "asked by the person", in.IDs...)
	})
	a := app.NewApp("wiki", svc).With(app.Database("main", sqlite.Engine(), app.Keeps(app.Privacy, editors)),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard))
	run(t, a)
	ctx := app.WithUser(t.Context(), "u1", struct{}{})

	if err := editors.Insert(ctx, editor{ID: "e1", Email: "ann@wiki.test", Name: "Annabel Quist"}); err != nil {
		t.Fatal(err)
	}
	if _, err := editors.Update(ctx, "e1", func(e *editor) error { e.Name = "Annabel Vesper"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := editors.Insert(ctx, editor{ID: "e2", Email: "bob@wiki.test", Name: "Robert Vale"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"e1", "e2"} {
		if err := editors.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	db := open(t, filepath.Join(dir, "main.sqlite"))
	trashRows := func() map[string][]byte {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT doc_key, doc FROM "wiki__editors___vs" WHERE num = 0`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for rows.Next() {
			var key, doc []byte
			if err := rows.Scan(&key, &doc); err != nil {
				t.Fatal(err)
			}
			out[string(key)] = doc
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rows := trashRows()
	if len(rows) != 2 {
		t.Fatalf("the trash's rows after two deletions: %d", len(rows))
	}
	for key, doc := range rows {
		if !bytes.Contains(doc, []byte(`"sealed:v1:`)) || bytes.Contains(doc, []byte("Annabel")) || bytes.Contains(doc, []byte("@wiki.test")) {
			t.Errorf("the trashed %s rests in clear: %s", key, doc)
		}
	}

	out, err := erase.Dispatch(ctx, erasing{IDs: []string{"ann@wiki.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stores) != 1 || !slices.Equal(out.Stores[0].Deleted, []string{"e1"}) {
		t.Errorf("the erasure: %+v, want e1 deleted from the trash", out)
	}
	if _, ok := trashRows()["e1"]; ok {
		t.Error("an erased person's record is still in the trash")
	}
	var left int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "wiki__editors___vs" WHERE doc_key = ?`, []byte("e1")).Scan(&left); err != nil || left != 0 {
		t.Errorf("an erased person's versions: %d rows, %v", left, err)
	}
	stones, err := editors.Trashed(ctx)
	if err != nil || len(stones) != 1 || stones[0].Key != "e2" {
		t.Errorf("the trash after the erasure: %+v %v, want bob's alone", stones, err)
	}
	back, err := editors.Untrash(ctx, "e2")
	if err != nil || back.Name != "Robert Vale" || back.Email != "bob@wiki.test" {
		t.Errorf("another person's record, untrashed: %+v %v", back, err)
	}
}
