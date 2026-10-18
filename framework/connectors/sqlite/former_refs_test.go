package sqlite_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The product testdata/former-refs was written by (ADR 0180 step 5c-2): the
// release before the references at rest were re-sealed. It is
// testdata/former-journal migrated by that release, then:
//   - ivy's case, whose subject became jay: its former note sealed under
//     ivy's data key, the case under jay's;
//   - kim's case, held, then written again: sealed under its own key, its
//     former note and versions too;
//   - leo's case, in the trash;
//   - a letter to kim kept in the queue.
// Every data key is named by a reference under the kit/v1 labels, the holds
// are filed under kit/v1 record references and sealed bound to
// kit.privacy/store/holds, and the subject index is kit:subject.

// refsKeys are every case the refs dump reads the versions and former values
// of.
var refsKeys = []string{"c-ann", "c-bob", "c-cy", "c-dan", "c-eve", "c-fay", "c-ivy", "c-kim", "c-leo"}

// refsPeople are every identity the refs dump exports.
var refsPeople = []string{"ann@atelier.test", "bob@atelier.test", "cy@atelier.test", "dan@atelier.test", "eve@atelier.test", "fay@atelier.test", "ivy@atelier.test", "jay@atelier.test", "kim@atelier.test", "leo@atelier.test"}

// refsDump is what the product reads of what it keeps, wider than the
// atelier's dump: every case's versions and former note, the trash, and
// every person's export.
type refsDump struct {
	Cases     []atelierCase                `json:"cases"`
	Tally     []tallyRow                   `json:"tally"`
	Revisions map[string][]atelierRevision `json:"revisions"`
	Former    map[string][]app.Former      `json:"former"`
	Trashed   []app.Tombstone              `json:"trashed"`
	Exports   map[string]app.PersonalData  `json:"exports"`
}

// dumpRefs reads what the running product keeps.
func dumpRefs(t *testing.T, a *atelier) []byte {
	t.Helper()
	ctx := t.Context()
	d := refsDump{Revisions: map[string][]atelierRevision{}, Former: map[string][]app.Former{}, Exports: map[string]app.PersonalData{}}
	var err error
	if d.Cases, err = a.cases.List(ctx); err != nil {
		t.Fatalf("the cases: %v", err)
	}
	slices.SortFunc(d.Cases, func(x, y atelierCase) int { return compare(x.ID, y.ID) })
	if d.Tally, err = a.tally.List(ctx); err != nil {
		t.Fatalf("the tally: %v", err)
	}
	for _, key := range refsKeys {
		revs, err := a.cases.Revisions(ctx, key)
		if ke, ok := errors.AsType[*app.Error](err); ok && ke.Code == app.WireNotFound {
			continue // erased from the trash: it keeps nothing
		}
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
	for _, who := range refsPeople {
		if d.Exports[who], err = a.export.Dispatch(ctx, atelierWho{ID: who}); err != nil {
			t.Fatalf("the export of %s: %v", who, err)
		}
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// boxPattern finds a box in what rests: a member sealed under a data key.
var boxPattern = regexp.MustCompile(`sealed:v1:([A-Za-z0-9_-]+)`)

// boxRefs are the data keys the boxes at rest in dir name, each with where
// one rests — a file of the data directory, or a table of its SQLite file —,
// the secrets and the store of data keys left out.
func boxRefs(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	add := func(where string, b []byte) {
		for _, m := range boxPattern.FindAllSubmatch(b, -1) {
			raw, err := base64.RawURLEncoding.DecodeString(string(m[1]))
			if err != nil {
				continue // a box under data-key itself, "root:…", names no key
			}
			ref, err := secret.SubjectOf(raw)
			if err != nil {
				continue
			}
			if !slices.Contains(out[ref], where) {
				out[ref] = append(out[ref], where)
			}
		}
	}
	for rel, b := range treeOf(t, dir) {
		if strings.HasPrefix(rel, ".secrets/") || strings.HasPrefix(rel, "app.privacy/keys") {
			continue
		}
		add(rel, b)
	}
	for table, rows := range tablesOf(t, dir) {
		for _, row := range rows {
			for col := range strings.SplitSeq(row, "|") {
				if b, err := hex.DecodeString(col); err == nil {
					add("table "+table, b)
				}
			}
		}
	}
	return out
}

// refsLabels are the two sets of labels, by the release that made them.
var refsLabels = map[string]struct{ subject, record string }{
	"kit": {"kit/v1 subject reference", "kit/v1 record reference"},
	"app": {"app/v1 subject reference", "app/v1 record reference"},
}

// refNames are the names of the data keys the fixture's people and records
// have under the labels of release — "kit", the former, or "app" —, each
// with what it is: "person ann@atelier.test", "own atelier/store/cases c-kim".
func refNames(t *testing.T, ikm []byte, release string) map[string]string {
	t.Helper()
	l := refsLabels[release]
	out := map[string]string{}
	for _, who := range refsPeople {
		out["s:"+reference(t, ikm, l.subject, who)] = "person " + who
	}
	for _, key := range refsKeys {
		out["r:"+reference(t, ikm, l.record, "atelier/store/cases\x00"+key)] = "own atelier/store/cases " + key
	}
	out["r:"+reference(t, ikm, l.record, "atelier/store/tally\x00t1")] = "own atelier/store/tally t1"
	return out
}

// relabeled is out — what `privacy holds` printed — with every reference of
// the fixture's records and people made under the kit/v1 labels replaced by
// the one under app/v1: what the holds say once filed again.
func relabeled(t *testing.T, ikm []byte, out []byte) []byte {
	t.Helper()
	text := string(out)
	for _, who := range refsPeople {
		text = strings.ReplaceAll(text, reference(t, ikm, refsLabels["kit"].subject, who), reference(t, ikm, refsLabels["app"].subject, who))
	}
	for _, rec := range []string{"atelier/store/cases\x00c-ann", "atelier/store/cases\x00c-eve", "atelier/store/cases\x00c-kim", "atelier/store/tally\x00t1"} {
		text = strings.ReplaceAll(text, reference(t, ikm, refsLabels["kit"].record, rec), reference(t, ikm, refsLabels["app"].record, rec))
	}
	return []byte(text)
}

// indexKeyOf is the index key of the data in dir.
func indexKeyOf(t *testing.T, dir string) []byte {
	t.Helper()
	store := keyedSecrets(t, dir)
	defer closeStore(store)
	return []byte(secretValue(t, store, "app-index-key"))
}

// What a former release sealed under its references (testdata/former-refs,
// written before ADR 0180 step 5c-2) is read the first time this release
// meets it as that release read it — the cases, their versions, former values
// and trash, every person's export, the erased erased, the held held, the
// kept letters and the notice delivered —, and the start seals it again under
// the present references: no box names a key a former release named but the
// former note of ivy's case, under ivy's key — the record names jay now, and
// nothing names ivy any more —, the holds are filed under their records'
// present references, and `privacy holds` says them so. A second start moves
// nothing and reads the same.
func TestFormerReferencesAreSealedAgainAndReadAsTheyWere(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-refs", kind)
			dir := keyedCopy(t, fixture)
			ikm := indexKeyOf(t, dir)
			formerNames, presentNames := refNames(t, ikm, "kit"), refNames(t, ikm, "app")
			before := boxRefs(t, dir)
			if _, ok := before[keyByWhat(formerNames, "person dan@atelier.test")]; !ok {
				t.Fatalf("the fixture holds no box under dan's former key: %v", slices.Sorted(maps.Keys(before)))
			}
			wantHolds, err := os.ReadFile(filepath.Join(fixture, "holds.txt"))
			if err != nil {
				t.Fatal(err)
			}
			wantJournal, err := os.ReadFile(filepath.Join(fixture, "journal.txt"))
			if err != nil {
				t.Fatal(err)
			}
			for args, want := range map[string][]byte{"holds": wantHolds, "journal -verify": wantJournal} {
				status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, append([]string{"privacy"}, strings.Fields(args)...)...)
				if status != 0 || !bytes.Equal(got, want) {
					t.Errorf("privacy %s before the start: %d\n got %s\nwant %s", args, status, got, want)
				}
			}
			want, err := os.ReadFile(filepath.Join(fixture, "dump.json"))
			if err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				k := newKeyed(t, dir, onSQLite, false)
				run(t, k.app)
				if round == 0 {
					letters := map[string]bool{}
					for range 3 {
						letters[received(t, "a letter kept in the queue", k.posted).To] = true
					}
					if len(letters) != 3 || !letters["kim@atelier.test"] {
						t.Errorf("the letters kept: %v", letters)
					}
					if got := received(t, "the notice kept in the queue", k.pinned); got.Text != "a notice about nobody, kept" {
						t.Errorf("the notice kept: %+v", got)
					}
				}
				if got := dumpRefs(t, k.atelier); !bytes.Equal(got, want) {
					t.Errorf("round %d, what the product reads:\n got %s\nwant %s", round, got, want)
				}
				if err := k.app.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
				after := boxRefs(t, dir)
				for ref, where := range after {
					if what, former := formerNames[ref]; former && !leftUnder(what, where) {
						t.Errorf("round %d: %s's former key still seals boxes in %v", round, what, where)
					}
				}
				for _, what := range []string{"person dan@atelier.test", "person jay@atelier.test", "own atelier/store/cases c-ann", "own atelier/store/cases c-eve", "own atelier/store/cases c-kim"} {
					if _, ok := after[keyByWhat(presentNames, what)]; !ok {
						t.Errorf("round %d: no box under %s's present key", round, what)
					}
				}
			}
			if status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "holds"); status != 0 || !bytes.Equal(got, relabeled(t, ikm, wantHolds)) {
				t.Errorf("privacy holds once filed again: %d\n got %s\nwant %s", status, got, relabeled(t, ikm, wantHolds))
			}
			if status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "journal", "-verify"); status != 0 || !strings.Contains(string(got), "the chain holds") {
				t.Errorf("privacy journal -verify once sealed again: %d\n%s", status, got)
			}
		})
	}
}

// leftUnder reports whether boxes under the former key of what, resting in
// where, are what the pass leaves where they lie: ivy's former note and
// version, under a person the record no longer names; the versions of a
// record in the trash, bob's and leo's, which the SDK does not rewrite.
func leftUnder(what string, where []string) bool {
	kept := func(allowed ...string) bool {
		return !slices.ContainsFunc(where, func(w string) bool {
			return !slices.ContainsFunc(allowed, func(a string) bool { return strings.Contains(w, a) })
		})
	}
	switch what {
	case "person ivy@atelier.test":
		return kept("history", "versions", "___vs")
	case "person bob@atelier.test", "person leo@atelier.test":
		return kept("versions", "___vs")
	}
	return false
}

// keyByWhat is the name names gives what.
func keyByWhat(names map[string]string, what string) string {
	for name, w := range names {
		if w == what {
			return name
		}
	}
	return ""
}

// noteOf reads what the product keeps of the people ids: every case's
// versions and former notes, the trash, and their exports, as text.
func noteOf(t *testing.T, dir string, onSQLite bool) string {
	t.Helper()
	k := newKeyed(t, dir, onSQLite, false)
	run(t, k.app)
	defer func() {
		if err := k.app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}()
	return string(dumpRefs(t, k.atelier))
}

// An erasure once the references are sealed again is an erasure at once, by
// every way the product reads and whatever a backup kept: dan, ivy and leo
// erased — dan's case under his present key, ivy's former note under her
// former one, leo's case in the trash — read as erased, their two data keys
// destroyed and none made again; a backup of the data taken before the
// erasure, before the pass or after it, restored beside the keys as the
// erasure left them, opens nothing of them either. kim, held, is kept.
func TestAnErasureAfterTheReSealDestroysBothKeys(t *testing.T) {
	needsFileStore(t)
	gone := []string{"dan@atelier.test", "ivy@atelier.test", "leo@atelier.test"}
	notes := []string{"a note about dan", "a note about ivy\"", "a note about leo"}
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-refs", kind)
			dir := keyedCopy(t, fixture)
			beforePass := treeCopy(t, dir)
			for _, note := range notes {
				if !strings.Contains(noteOf(t, dir, onSQLite), note) {
					t.Fatalf("%q does not read before the erasure", note)
				}
			}
			afterPass := treeCopy(t, dir)
			k := newKeyed(t, dir, onSQLite, false)
			run(t, k.app)
			for _, who := range append(slices.Clone(gone), "kim@atelier.test") {
				if _, err := k.forget.Dispatch(t.Context(), atelierWho{ID: who}); err != nil {
					t.Fatalf("forget %s: %v", who, err)
				}
			}
			if err := k.app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			ikm := indexKeyOf(t, dir)
			names := dataKeyNames(t, dir)
			for _, release := range []string{"kit", "app"} {
				for _, who := range gone {
					if ref := "s:" + reference(t, ikm, refsLabels[release].subject, who); slices.Contains(names, ref) {
						t.Errorf("%s's %s data key outlived the erasure", who, release)
					}
				}
			}
			for name, data := range map[string]string{
				"now":                           dir,
				"a backup from before the pass": restoredBeside(t, beforePass, dir),
				"a backup from after the pass":  restoredBeside(t, afterPass, dir),
			} {
				read := noteOf(t, data, onSQLite)
				for _, note := range notes {
					if strings.Contains(read, note) {
						t.Errorf("%s: %q reads after the erasure", name, note)
					}
				}
				if !strings.Contains(read, "a second note about kim") {
					t.Errorf("%s: kim's held case no longer reads", name)
				}
				if got := dataKeyNames(t, data); !slices.Equal(got, names) {
					t.Errorf("%s: the data keys changed: a key was made again: %v, were %v", name, got, names)
				}
			}
		})
	}
}
