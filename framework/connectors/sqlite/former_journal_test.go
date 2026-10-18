package sqlite_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The product testdata/former-journal was written by (ADR 0180 step 5c): the
// release before the journal was closed — testdata/former-keys migrated by
// it, then eve's case written twice and held, fay's case written and fay
// erased, eve exported, a letter to eve kept in the queue, and ann exported —,
// with what it read of it. Its privacy journal holds ten entries, whose
// references are made under the kit/v1 labels and whose reasons are sealed
// bound to kit.privacy/store/journal. The product is keyedAtelier's.

// journalDocs are the privacy journal's entries as they rest in dir, by key,
// each parsed: from its table on the SQLite file when onSQLite, from its
// file in the data directory otherwise.
func journalDocs(t *testing.T, dir string, onSQLite bool) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	parse := func(key string, raw []byte) {
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("journal entry %s: %v", key, err)
		}
		out[key] = doc
	}
	if onSQLite {
		db := openMain(t, dir)
		defer db.Close()
		rows, err := db.QueryContext(t.Context(), `SELECT doc_key, doc FROM app_privacy__journal`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var key, doc []byte
			if err := rows.Scan(&key, &doc); err != nil {
				t.Fatal(err)
			}
			parse(string(key), doc)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	raw, err := os.ReadFile(filepath.Join(dir, "app.privacy", "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var docs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &docs); err != nil {
		t.Fatal(err)
	}
	for key, doc := range docs {
		parse(key, doc)
	}
	if pending, _ := os.ReadDir(filepath.Join(dir, "app.privacy", "journal.json.d")); len(pending) > 0 {
		t.Fatalf("the journal's writes were not folded at the stop: %d left", len(pending))
	}
	return out
}

// seqKey is the journal's key of entry seq: its number, zero-padded.
func seqKey(seq int) string { return fmt.Sprintf("%020d", seq) }

// reference is the reference of text under the index key ikm and label, as
// the framework makes it: HMAC-SHA256 under the label's HKDF-SHA256 subkey
// of 32 bytes, its first 16 bytes in hex — computed here with the standard
// library alone, so that a reference the fixture holds checks the formula.
func reference(t *testing.T, ikm []byte, label, text string) string {
	t.Helper()
	key, err := hkdf.Key(sha256.New, ikm, nil, label, 32)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// rows are the lines of a command's output, each its fields.
func rows(out []byte) [][]string {
	var all [][]string
	for line := range strings.Lines(string(out)) {
		all = append(all, strings.Fields(line))
	}
	return all
}

// The journal a former release kept (testdata/former-journal, written before
// ADR 0180 step 5c) is read as it was until this release journals, then
// closed as it is: its ten entries stay byte for byte, the next entry is a
// JournalContinue entry naming the tenth's hash, and every entry after it
// names its references under the app/v1 labels. `privacy journal -verify`
// verifies the former journal, then the next, and the link between them;
// `privacy holds`, the cases, versions, former values, trash and a person's
// data read as the former release read them, the erased people erased, the
// kept letters delivered. A second start closes nothing again.
func TestAFormerJournalIsClosedAsItWasAndContinued(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-journal", kind)
			dir := keyedCopy(t, fixture)
			former := journalDocs(t, dir, onSQLite)
			if len(former) != 10 {
				t.Fatalf("the fixture's journal holds %d entries, want 10", len(former))
			}
			wantJournal, err := os.ReadFile(filepath.Join(fixture, "journal.txt"))
			if err != nil {
				t.Fatal(err)
			}
			wantHolds, err := os.ReadFile(filepath.Join(fixture, "holds.txt"))
			if err != nil {
				t.Fatal(err)
			}

			// The commands read it as the former release did: nothing is
			// journaled, nothing is closed.
			for args, want := range map[string][]byte{"journal -verify": wantJournal, "holds": wantHolds} {
				status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, append([]string{"privacy"}, strings.Fields(args)...)...)
				if status != 0 || !bytes.Equal(got, want) {
					t.Errorf("privacy %s: %d\n got %s\nwant %s", args, status, got, want)
				}
			}
			if got := journalDocs(t, dir, onSQLite); !reflect.DeepEqual(got, former) {
				t.Fatalf("a command changed the former journal")
			}

			for round := range 2 {
				k := readsAsItWas(t, dir, fixture, onSQLite)
				if round == 0 {
					letters := map[string]bool{}
					for range 2 {
						letters[received(t, "a letter kept in the queue", k.posted).To] = true
					}
					if !letters["bob@atelier.test"] || !letters["eve@atelier.test"] {
						t.Errorf("the letters kept: %v", letters)
					}
					if got := received(t, "the notice kept in the queue", k.pinned); got.Text != "a notice about nobody, kept" {
						t.Errorf("the notice kept: %+v", got)
					}
				}
				fay, err := k.export.Dispatch(t.Context(), atelierWho{ID: "fay@atelier.test"})
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range fay.Stores {
					if len(s.Records) > 0 {
						t.Errorf("fay, erased by the former release, opened again: %+v", s)
					}
				}
				if err := k.app.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}

			docs := journalDocs(t, dir, onSQLite)
			for key, doc := range former {
				if !reflect.DeepEqual(docs[key], doc) {
					t.Errorf("former entry %s changed:\n got %v\nwant %v", key, docs[key], doc)
				}
			}
			cont := docs[seqKey(11)]
			if cont["op"] != "continue" || cont["follows"] != former[seqKey(10)]["hash"] || cont["refs"] != "app/v1" || cont["by"] != "app" {
				t.Errorf("entry 11 does not continue the former journal: %v", cont)
			}
			continues := 0
			for seq := 11; seq <= len(docs); seq++ {
				e := docs[seqKey(seq)]
				if e["refs"] != "app/v1" {
					t.Errorf("entry %d is not under the app/v1 labels: %v", seq, e)
				}
				if e["op"] == "continue" {
					continues++
				}
			}
			if continues != 1 {
				t.Errorf("%d entries continue a journal, want 1: a second start closed it again", continues)
			}

			// The references: the former journal names ann under kit/v1, the
			// next under app/v1, both from the same index key.
			ikm := []byte(secretValue(t, keyedSecrets(t, dir), "app-index-key"))
			oldAnn, newAnn := reference(t, ikm, "kit/v1 subject reference", "ann@atelier.test"), reference(t, ikm, "app/v1 subject reference", "ann@atelier.test")
			if former[seqKey(10)]["subject"] != oldAnn {
				t.Errorf("the former journal's export of ann names %v, want her kit/v1 reference %s", former[seqKey(10)]["subject"], oldAnn)
			}
			if e := docs[seqKey(12)]; e["op"] != "export" || e["subject"] != newAnn || newAnn == oldAnn {
				t.Errorf("the next journal's export of ann: %v, want her app/v1 reference %s", e, newAnn)
			}

			status, out := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "journal", "-verify")
			if status != 0 {
				t.Fatalf("privacy journal -verify: %d\n%s", status, out)
			}
			got, want := rows(out), rows(wantJournal)
			for i := range 11 { // the header and the ten former entries
				if !reflect.DeepEqual(got[i], want[i]) {
					t.Errorf("line %d: %v, the former release printed %v", i, got[i], want[i])
				}
			}
			for _, line := range []string{
				"a journal closed at entry 10: entry 11 names its hash and continues it\n",
				fmt.Sprintf("the chain holds: %d entries\n", len(docs)),
			} {
				if !strings.Contains(string(out), line) {
					t.Errorf("privacy journal -verify does not say %q:\n%s", line, out)
				}
			}
			// The holds, filed again under their records' present references
			// by the start (ADR 0180 step 5c-2), say the same.
			if status, got := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "holds"); status != 0 || !bytes.Equal(got, relabeled(t, ikm, wantHolds)) {
				t.Errorf("privacy holds once closed: %d\n got %s\nwant %s", status, got, relabeled(t, ikm, wantHolds))
			}
		})
	}
}

// editJournal rewrites entry key of the journal in dir, the product
// stopped, with edit.
func editJournal(t *testing.T, dir string, onSQLite bool, key string, edit func(map[string]any)) {
	t.Helper()
	docs := journalDocs(t, dir, onSQLite)
	doc, ok := docs[key]
	if !ok {
		t.Fatalf("the journal holds no entry %s", key)
	}
	edit(doc)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if onSQLite {
		db := openMain(t, dir)
		defer db.Close()
		if _, err := db.ExecContext(t.Context(), `UPDATE app_privacy__journal SET doc = ? WHERE doc_key = ?`, raw, []byte(key)); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(dir, "app.privacy", "journal.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	all[key] = raw
	if b, err = json.Marshal(all); err == nil {
		err = os.WriteFile(path, b, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// Closed, the former journal is still held by the chain: an entry of it
// changed breaks the chain there, and the link changed — entry 11 naming
// another hash — breaks it at entry 11.
func TestAClosedFormerJournalStillBreaksWhereItIsChanged(t *testing.T) {
	needsFileStore(t)
	type tc struct {
		kind, name string
		key        string
		edit       func(map[string]any)
		at         string
	}
	runCase := func(t *testing.T, c tc) {
		onSQLite := c.kind == "sqlite"
		fixture := filepath.Join("testdata", "former-journal", c.kind)
		dir := keyedCopy(t, fixture)
		if err := readsAsItWas(t, dir, fixture, onSQLite).app.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		editJournal(t, dir, onSQLite, c.key, c.edit)
		status, out := mainOf(t, newKeyed(t, dir, onSQLite, false).atelier, "privacy", "journal", "-verify")
		if status == 0 || !strings.Contains(string(out), c.at) {
			t.Errorf("privacy journal -verify with %s changed: %d\n%s", c.name, status, out)
		}
	}
	formerEntry := func(e map[string]any) { e["by"] = "studio" }
	link := func(e map[string]any) { e["follows"] = strings.Repeat("0", 64) }
	for _, c := range []tc{
		{"sqlite", "a former entry", seqKey(3), formerEntry, "the chain breaks at entry 3"},
		{"sqlite", "the link", seqKey(11), link, "the chain breaks at entry 11"},
		{"files", "a former entry", seqKey(3), formerEntry, "the chain breaks at entry 3"},
		{"files", "the link", seqKey(11), link, "the chain breaks at entry 11"},
	} {
		t.Run(c.kind+"/"+c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// A close the disk refuses — the journal's directory read-only when the
// first entry of this release is written — leaves the former journal as it
// was and readable, the operation that journaled done; once the disk takes
// writes again, the next entry closes it, once, as an uninterrupted close
// does.
func TestAFormerJournalsCloseTheDiskRefusesResumes(t *testing.T) {
	needsFileStore(t)
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only directory refuses no write here")
	}
	fixture := filepath.Join("testdata", "former-journal", "files")
	dir := keyedCopy(t, fixture)
	former := journalDocs(t, dir, false)
	pending := filepath.Join(dir, "app.privacy", "journal.json.d")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	k := newKeyed(t, dir, false, false)
	run(t, k.app)
	if err := os.Chmod(pending, 0o500); err != nil {
		t.Fatal(err)
	}
	// The export stands; its entry is refused, and the journal checked
	// below shows nothing of it was written — neither the entry nor the
	// one that would have closed the former journal.
	if _, err := k.export.Dispatch(t.Context(), atelierWho{ID: "ann@atelier.test"}); err != nil {
		t.Logf("the export: %v", err)
	}
	if err := os.Chmod(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := k.app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := journalDocs(t, dir, false); !reflect.DeepEqual(got, former) {
		t.Fatalf("the refused close changed the journal: %d entries", len(got))
	}
	if err := readsAsItWas(t, dir, fixture, false).app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	docs := journalDocs(t, dir, false)
	if docs[seqKey(11)]["op"] != "continue" || docs[seqKey(11)]["follows"] != former[seqKey(10)]["hash"] {
		t.Errorf("entry 11 once resumed: %v", docs[seqKey(11)])
	}
	if status, out := mainOf(t, newKeyed(t, dir, false, false).atelier, "privacy", "journal", "-verify"); status != 0 || !strings.Contains(string(out), "a journal closed at entry 10") {
		t.Errorf("privacy journal -verify once resumed: %d\n%s", status, out)
	}
}

// dataKeyNames are the names of the data keys the store of data keys in dir
// keeps, folded at the stop.
func dataKeyNames(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "app.privacy", "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if pending, _ := os.ReadDir(filepath.Join(dir, "app.privacy", "keys.json.d")); len(pending) > 0 {
		t.Fatalf("the data keys' writes were not folded at the stop: %d left", len(pending))
	}
	return slices.Sorted(maps.Keys(keys))
}

// An erasure stays an erasure across the close: the people the former
// release erased — cy, fay — read as erased by every way the product reads,
// and dan, erased once the journal is continued, is gone from every way too
// — his case, its versions and former values, his export —, now and after a
// restart; ann, whose case a hold keeps, keeps it. The new journal names his
// key's destruction under his app/v1 reference; nothing is filed under an
// app/v1 reference in the store of data keys, so no key is made again for
// anyone under the new labels, and the erased have no key under the former
// ones either.
func TestAnErasureStaysAnErasureAcrossTheClose(t *testing.T) {
	needsFileStore(t)
	for _, kind := range []string{"sqlite", "files"} {
		t.Run(kind, func(t *testing.T) {
			onSQLite := kind == "sqlite"
			fixture := filepath.Join("testdata", "former-journal", kind)
			dir := keyedCopy(t, fixture)
			k := readsAsItWas(t, dir, fixture, onSQLite)
			if _, err := k.forget.Dispatch(t.Context(), atelierWho{ID: "dan@atelier.test"}); err != nil {
				t.Fatal(err)
			}
			if err := k.app.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				k := newKeyed(t, dir, onSQLite, false)
				run(t, k.app)
				dump := string(keyedDump(t, k))
				if !strings.Contains(dump, "a third note about ann") {
					t.Errorf("round %d: ann's held case no longer reads", round)
				}
				for _, gone := range []string{"note about dan", "note about cy", "note about fay"} {
					if strings.Contains(dump, gone) {
						t.Errorf("round %d: %q reads again", round, gone)
					}
				}
				for _, who := range []string{"dan@atelier.test", "cy@atelier.test", "fay@atelier.test"} {
					data, err := k.export.Dispatch(t.Context(), atelierWho{ID: who})
					if err != nil {
						t.Fatal(err)
					}
					for _, s := range data.Stores {
						if len(s.Records) > 0 {
							t.Errorf("round %d: %s, erased, exports %+v", round, who, s)
						}
					}
				}
				if err := k.app.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			ikm := []byte(secretValue(t, keyedSecrets(t, dir), "app-index-key"))
			newDan := reference(t, ikm, "app/v1 subject reference", "dan@atelier.test")
			shredded := false
			for _, e := range journalDocs(t, dir, onSQLite) {
				if e["op"] == "shred" && e["subject"] == newDan && e["refs"] == "app/v1" {
					shredded = true
				}
			}
			if !shredded {
				t.Errorf("the new journal does not name dan's key destroyed under his app/v1 reference")
			}
			names := dataKeyNames(t, dir)
			for _, who := range []string{"ann@atelier.test", "bob@atelier.test", "cy@atelier.test", "dan@atelier.test", "eve@atelier.test", "fay@atelier.test"} {
				for _, label := range []string{"app/v1 subject reference", "app/v1 record reference"} {
					ref := reference(t, ikm, label, who)
					if slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, ref) }) {
						t.Errorf("a data key is filed under %s's %s", who, label)
					}
				}
			}
			for _, who := range []string{"dan@atelier.test", "cy@atelier.test", "fay@atelier.test"} {
				if slices.Contains(names, "s:"+reference(t, ikm, "kit/v1 subject reference", who)) {
					t.Errorf("%s, erased, still has a data key", who)
				}
			}
		})
	}
}
