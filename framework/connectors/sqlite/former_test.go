package sqlite_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
)

// The product testdata/former was written by (ADR 0180 step 5): a release
// before the names at rest changed kept it under kit_* tables, a kit.privacy
// directory and a kit.privacy service. The declarations below are the ones
// that wrote it; a test that opens the fixture declares the same product, so
// that what it reads back is what the former release kept.

// atelierCase is a person's case: sealed where it rests, its note's former
// values kept, its versions and its trash kept.
type atelierCase struct {
	ID    string `json:"id"`
	Email string `json:"email" app:"subject"`
	Note  string `json:"note" app:"personal,history=3"`
	Stage int    `json:"stage"`
}

// atelierLetter is a letter about a person, sealed under their data key in
// its subscription's queue.
type atelierLetter struct {
	To   string `json:"to" app:"subject"`
	Body string `json:"body" app:"personal"`
}

// atelierWho names the person a privacy command is about.
type atelierWho struct {
	ID string `json:"id"`
}

// atelier is the product: its cases and its tally, a topic of letters and
// the privacy commands, on a SQLite database or in the data directory.
type atelier struct {
	app     *app.App
	cases   *app.Store[atelierCase]
	tally   *app.Store[tallyRow]
	letters *app.Topic[atelierLetter]
	export  *app.Command[atelierWho, app.PersonalData]
	forget  *app.Command[atelierWho, app.Erasure]
	// posted are the letters delivered, opened; with wait set, the
	// subscriber holds every letter until its context ends, and none is.
	posted chan atelierLetter
}

// atelierEnv pins what the product reads from its environment: its secrets
// kept in memory, its data key and its index key given.
func atelierEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ATELIER_SECRETS", "memory")
	t.Setenv("ATELIER_DATA_KEY", "an atelier data key, 32 bytes...")
	t.Setenv("ATELIER_INDEX_KEY", "an atelier index key of more than 16 bytes")
}

// newAtelier declares the product with its data in dir: on the SQLite
// database main — app.Privacy and the service, its queues included — when
// onSQLite, in the data directory otherwise.
func newAtelier(t *testing.T, dir string, onSQLite, wait bool) *atelier {
	t.Helper()
	atelierEnv(t)
	svc := app.NewService("atelier", "Cases and letters about people, sealed where they rest.")
	a := &atelier{posted: make(chan atelierLetter, 8)}
	a.cases = svc.Store("cases", func(c atelierCase) string { return c.ID }, app.Purpose("Handle the cases"), app.Revisions(3))
	a.tally = svc.Store("tally", func(r tallyRow) string { return r.ID })
	a.letters = svc.Topic[atelierLetter]("letters")
	svc.Subscribe("post", a.letters, func(ctx context.Context, l atelierLetter) error {
		if wait {
			<-ctx.Done()
			return ctx.Err()
		}
		a.posted <- l
		return nil
	})
	a.export = svc.Command("export", func(ctx context.Context, w atelierWho) (app.PersonalData, error) {
		return app.Export(ctx, w.ID)
	})
	a.forget = svc.Command("forget", func(ctx context.Context, w atelierWho) (app.Erasure, error) {
		return app.Erase(ctx, "the person asked", w.ID)
	})
	opts := []app.AppOption{app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard)}
	if onSQLite {
		opts = append(opts, app.Database("main", sqlite.Engine(), app.Keeps(app.Privacy, svc)))
	}
	a.app = app.NewApp("atelier", svc).With(opts...)
	return a
}

// atelierRevision is one version of a case, as the dump writes it.
type atelierRevision struct {
	Number  uint64      `json:"number"`
	At      string      `json:"at"`
	By      string      `json:"by"`
	Command string      `json:"command"`
	Value   atelierCase `json:"value"`
}

// atelierDump is what the product reads of what it keeps: every case, the
// versions and the note's former values of each, the trash, and a person's
// data as app.Export gives it.
type atelierDump struct {
	Cases     []atelierCase                `json:"cases"`
	Tally     []tallyRow                   `json:"tally"`
	Revisions map[string][]atelierRevision `json:"revisions"`
	Former    map[string][]app.Former      `json:"former"`
	Trashed   []app.Tombstone              `json:"trashed"`
	Export    app.PersonalData             `json:"export"`
}

// atelierKeys are the cases the dump reads the versions and former values
// of: kept, trashed and erased.
var atelierKeys = []string{"c-ann", "c-bob", "c-cy"}

// dumpAtelier reads what the running product keeps.
func dumpAtelier(t *testing.T, a *atelier) []byte {
	t.Helper()
	ctx := t.Context()
	d := atelierDump{Revisions: map[string][]atelierRevision{}, Former: map[string][]app.Former{}}
	var err error
	if d.Cases, err = a.cases.List(ctx); err != nil {
		t.Fatalf("the cases: %v", err)
	}
	slices.SortFunc(d.Cases, func(x, y atelierCase) int { return compare(x.ID, y.ID) })
	if d.Tally, err = a.tally.List(ctx); err != nil {
		t.Fatalf("the tally: %v", err)
	}
	for _, key := range atelierKeys {
		revs, err := a.cases.Revisions(ctx, key)
		if err != nil {
			t.Fatalf("the versions of %s: %v", key, err)
		}
		for _, r := range revs {
			d.Revisions[key] = append(d.Revisions[key], atelierRevision{Number: r.Number, At: r.At.UTC().Format("2006-01-02T15:04:05.999999999Z"), By: r.By, Command: r.Command, Value: r.Value})
		}
		if former, err := a.cases.Former(ctx, key, "/note"); err == nil {
			d.Former[key] = former
		}
	}
	if d.Trashed, err = a.cases.Trashed(ctx); err != nil {
		t.Fatalf("the trash: %v", err)
	}
	if d.Export, err = a.export.Dispatch(ctx, atelierWho{ID: "ann@atelier.test"}); err != nil {
		t.Fatalf("the export: %v", err)
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// compare orders two strings.
func compare(x, y string) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}
