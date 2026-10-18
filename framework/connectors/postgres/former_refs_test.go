package postgres_test

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/postgres"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The references at rest on PostgreSQL (ADR 0180 step 5c-2). What a former
// release kept is the SQLite suite's testdata/former-refs, as it rests in the
// data directory alone, copied onto the database by `data copy` — which
// copies boxes as they are, under the references a former release made —;
// the start then seals it again under the present ones, reads it as that
// release read it, and an erasure destroys both of a person's keys.

// refsTableQuery lists the tables of the test database.
const refsTableQuery = `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`

// refsFixture is the SQLite suite's fixture this one starts from.
var refsFixture = filepath.Join("..", "sqlite", "testdata", "former-refs", "files")

// refsCaseKeys and refsPeople are what the dump reads, as the SQLite suite's.
var (
	refsCaseKeys = []string{"c-ann", "c-bob", "c-cy", "c-dan", "c-eve", "c-fay", "c-ivy", "c-kim", "c-leo"}
	refsPeople   = []string{"ann@atelier.test", "bob@atelier.test", "cy@atelier.test", "dan@atelier.test", "eve@atelier.test", "fay@atelier.test", "ivy@atelier.test", "jay@atelier.test", "kim@atelier.test", "leo@atelier.test"}
)

// refsTally is a row of the tally.
type refsTally struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// refsNotice is a notice about nobody.
type refsNotice struct {
	Text string `json:"text" app:"personal"`
}

// refsAtelier is the SQLite suite's keyed atelier on PostgreSQL.
type refsAtelier struct {
	app    *app.App
	cases  *app.Store[atelierCase]
	tally  *app.Store[refsTally]
	export *app.Command[atelierWho, app.PersonalData]
	forget *app.Command[atelierWho, app.Erasure]
}

// newRefsAtelier declares it, its data in dir and on raw, its secrets in
// the file store beside its data.
func newRefsAtelier(t *testing.T, raw, dir string) *refsAtelier {
	t.Helper()
	for _, v := range []string{"ATELIER_SECRETS", "ATELIER_DATA_KEY", "ATELIER_INDEX_KEY", "ATELIER_SMTP_URL", "ATELIER_SECRETS_KEY"} {
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ATELIER_MAIN_URL", raw)
	svc := app.NewService("atelier", "Cases and letters about people, sealed where they rest.")
	a := &refsAtelier{}
	a.cases = svc.Store("cases", func(c atelierCase) string { return c.ID }, app.Purpose("Handle the cases"), app.Revisions(3))
	a.tally = svc.Store("tally", func(r refsTally) string { return r.ID })
	letters := svc.Topic[atelierLetter]("letters")
	notices := svc.Topic[refsNotice]("notices")
	svc.Subscribe("post", letters, func(context.Context, atelierLetter) error { return nil })
	svc.Subscribe("pin", notices, func(context.Context, refsNotice) error { return nil })
	a.export = svc.Command("export", func(ctx context.Context, w atelierWho) (app.PersonalData, error) {
		return app.Export(ctx, w.ID)
	})
	a.forget = svc.Command("forget", func(ctx context.Context, w atelierWho) (app.Erasure, error) {
		return app.Erase(ctx, "the person asked", w.ID)
	})
	a.app = app.NewApp("atelier", svc).With(app.Database("main", postgres.Engine(), app.Keeps(app.Privacy, svc)),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard))
	return a
}

// refsDataDir copies the fixture's data directory into one of the test's,
// its secret store under the name the product keeps it.
func refsDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	src := filepath.Join(refsFixture, "data")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dir, strings.Replace(rel, "_secrets", ".secrets", 1))
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// refsMain runs the product's binary with args, its standard output
// captured.
func refsMain(t *testing.T, a *refsAtelier, args ...string) (int, []byte) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	status := a.app.Main(t.Context(), args)
	os.Stdout = saved
	_ = w.Close()
	return status, <-done
}

// refsDump is what the product reads, as the SQLite suite's dumpRefs writes
// it.
func refsDump(t *testing.T, a *refsAtelier) []byte {
	t.Helper()
	ctx := t.Context()
	type revision struct {
		Number  uint64      `json:"number"`
		At      string      `json:"at"`
		By      string      `json:"by"`
		Command string      `json:"command"`
		Value   atelierCase `json:"value"`
	}
	d := struct {
		Cases     []atelierCase               `json:"cases"`
		Tally     []refsTally                 `json:"tally"`
		Revisions map[string][]revision       `json:"revisions"`
		Former    map[string][]app.Former     `json:"former"`
		Trashed   []app.Tombstone             `json:"trashed"`
		Exports   map[string]app.PersonalData `json:"exports"`
	}{Revisions: map[string][]revision{}, Former: map[string][]app.Former{}, Exports: map[string]app.PersonalData{}}
	var err error
	if d.Cases, err = a.cases.List(ctx); err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(d.Cases, func(x, y atelierCase) int { return strings.Compare(x.ID, y.ID) })
	if d.Tally, err = a.tally.List(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range refsCaseKeys {
		revs, err := a.cases.Revisions(ctx, key)
		if ke, ok := errors.AsType[*app.Error](err); ok && ke.Code == app.WireNotFound {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range revs {
			d.Revisions[key] = append(d.Revisions[key], revision{r.Number, r.At.UTC().Format("2006-01-02T15:04:05.999999999Z"), r.By, r.Command, r.Value})
		}
		if former, err := a.cases.Former(ctx, key, "/note"); err == nil {
			d.Former[key] = former
		}
	}
	if d.Trashed, err = a.cases.Trashed(ctx); err != nil {
		t.Fatal(err)
	}
	for _, who := range refsPeople {
		if d.Exports[who], err = a.export.Dispatch(ctx, atelierWho{ID: who}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// refsReference is the reference of text under the index key ikm and label.
func refsReference(t *testing.T, ikm []byte, label, text string) string {
	t.Helper()
	key, err := hkdf.Key(sha256.New, ikm, nil, label, 32)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// refsBoxPattern finds a box under a data key.
var refsBoxPattern = regexp.MustCompile(`sealed:v1:([A-Za-z0-9_-]+)`)

// refsBoxes are the data keys the boxes the database keeps name, each with
// the tables one rests in.
func refsBoxes(t *testing.T, raw string) map[string][]string {
	t.Helper()
	db := open(t, raw)
	out := map[string][]string{}
	for _, table := range refsNames(t, raw, refsTableQuery) {
		if strings.HasPrefix(table, "app_privacy__keys") {
			continue
		}
		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+table+`"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				b, ok := v.([]byte)
				if !ok {
					s, isText := v.(string)
					b, ok = []byte(s), isText
				}
				if !ok {
					continue
				}
				for _, m := range refsBoxPattern.FindAllSubmatch(b, -1) {
					box, err := base64.RawURLEncoding.DecodeString(string(m[1]))
					if err != nil {
						continue
					}
					if ref, err := secret.SubjectOf(box); err == nil && !slices.Contains(out[ref], table) {
						out[ref] = append(out[ref], table)
					}
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// refsNames are the first column of query's rows, as text.
func refsNames(t *testing.T, raw, query string) []string {
	t.Helper()
	rows, err := open(t, raw).QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name []byte
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, string(name))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// refsLeft reports whether boxes under the former key of who, in tables, are
// what the pass leaves where they lie: ivy's former note and version, under a
// person the record no longer names; the versions of a record in the trash.
func refsLeft(who string, tables []string) bool {
	allowed := map[string][]string{
		"ivy@atelier.test": {"__history", "___vs"},
		"bob@atelier.test": {"___vs"},
		"leo@atelier.test": {"___vs"},
	}[who]
	return allowed != nil && !slices.ContainsFunc(tables, func(table string) bool {
		return !slices.ContainsFunc(allowed, func(a string) bool { return strings.HasSuffix(table, a) })
	})
}

func TestFormerReferencesOnPostgresAreSealedAgainAndReadAsTheyWere(t *testing.T) {
	raw := fresh(t)
	dir := refsDataDir(t)
	if status, out := refsMain(t, newRefsAtelier(t, raw, dir), "data", "copy", "--to", "main"); status != 0 {
		t.Fatalf("data copy: %d %s", status, out)
	}
	key, err := secret.KeyFile(filepath.Join(dir, ".secrets", "key"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := secret.NewFile(secret.FileConfig{Dir: filepath.Join(dir, ".secrets", "store"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	index, err := store.Get(t.Context(), "app-index-key")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := store.(io.Closer); ok {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ikm := index.Value.Reveal()
	formerRef := func(who string) string {
		return "s:" + refsReference(t, ikm, "kit/v1 subject reference", who)
	}
	if _, ok := refsBoxes(t, raw)[formerRef("dan@atelier.test")]; !ok {
		t.Fatal("the copy holds no box under dan's former key")
	}
	want, err := os.ReadFile(filepath.Join(refsFixture, "dump.json"))
	if err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		a := newRefsAtelier(t, raw, dir)
		if err := a.app.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := refsDump(t, a); !bytes.Equal(got, want) {
			t.Errorf("round %d, what the product reads:\n got %s\nwant %s", round, got, want)
		}
		if err := a.app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		boxes := refsBoxes(t, raw)
		for _, who := range refsPeople {
			if tables, ok := boxes[formerRef(who)]; ok && !refsLeft(who, tables) {
				t.Errorf("round %d: %s's former key still seals boxes in %v", round, who, tables)
			}
		}
		if _, ok := boxes["s:"+refsReference(t, ikm, "app/v1 subject reference", "dan@atelier.test")]; !ok {
			t.Errorf("round %d: no box under dan's present key", round)
		}
	}

	a := newRefsAtelier(t, raw, dir)
	if err := a.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"dan@atelier.test", "ivy@atelier.test", "leo@atelier.test"} {
		if _, err := a.forget.Dispatch(t.Context(), atelierWho{ID: who}); err != nil {
			t.Fatal(err)
		}
	}
	read := string(refsDump(t, a))
	if err := a.app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, note := range []string{"a note about dan", "a note about ivy\"", "a note about leo"} {
		if strings.Contains(read, note) {
			t.Errorf("%q reads after the erasure", note)
		}
	}
	keys := refsNames(t, raw, `SELECT doc_key FROM app_privacy__keys`)
	for _, who := range []string{"dan@atelier.test", "ivy@atelier.test", "leo@atelier.test"} {
		for _, label := range []string{"kit/v1 subject reference", "app/v1 subject reference"} {
			if ref := "s:" + refsReference(t, ikm, label, who); slices.Contains(keys, ref) {
				t.Errorf("%s's data key under %s outlived the erasure", who, label)
			}
		}
	}
}
