package mysql_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// The privacy journal on MySQL (ADR 0180 step 5c): a journal a former
// release kept in app.privacy's table is closed as it is by the next entry
// this release journals — a JournalContinue entry naming its last hash — and
// `privacy journal -verify` holds across the two, and breaks where the
// former is changed. The former journal is written into the table as a
// former release wrote it, entries without a reason, each chained as the
// journal documents it: SHA-256 of the previous hash, a newline, and the
// entry's JSON with an empty hash. The SQLite suite holds the rest on a
// former release's own data (testdata/former-journal).

// formerJournalEntry is an entry as a former release wrote it: no refs, no
// follows.
type formerJournalEntry struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Op      string    `json:"op"`
	Store   string    `json:"store,omitempty"`
	Subject string    `json:"subject,omitempty"`
	By      string    `json:"by"`
	Hash    string    `json:"hash"`
}

// writeFormerJournal writes n entries of a former release's journal into
// app.privacy's table on raw, and returns their documents by key.
func writeFormerJournal(t *testing.T, raw string, n int) map[string][]byte {
	t.Helper()
	db := open(t, raw)
	out := map[string][]byte{}
	prev := ""
	for i := 1; i <= n; i++ {
		e := formerJournalEntry{Seq: int64(i), At: time.Date(2026, 10, 17, 9, 0, i, 0, time.UTC), Op: "export", Store: "atelier/store/cases", Subject: fmt.Sprintf("%032x", i), By: "atelier/command/export"}
		unhashed, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(append([]byte(prev+"\n"), unhashed...))
		e.Hash = hex.EncodeToString(sum[:])
		doc, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%020d", i)
		if _, err := db.ExecContext(t.Context(), `INSERT INTO app_privacy__journal (doc_key, rev, doc) VALUES (?, 1, ?)`, []byte(key), doc); err != nil {
			t.Fatal(err)
		}
		out[key], prev = doc, e.Hash
	}
	return out
}

// journalOn is every document of app.privacy's journal on raw, by key.
func journalOn(t *testing.T, raw string) map[string][]byte {
	t.Helper()
	rows, err := open(t, raw).QueryContext(t.Context(), `SELECT doc_key, doc FROM app_privacy__journal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var key, doc []byte
		if err := rows.Scan(&key, &doc); err != nil {
			t.Fatal(err)
		}
		out[string(key)] = doc
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// verified runs `privacy journal -verify` on the atelier, its standard
// output captured.
func verified(t *testing.T, raw, dir string) (int, string) {
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
	status := newAtelier(t, raw, dir, false).app.Main(t.Context(), []string{"privacy", "journal", "-verify"})
	os.Stdout = saved
	_ = w.Close()
	return status, string(<-done)
}

// exported starts the atelier, writes ann's case, exports her, and stops
// it.
func exported(t *testing.T, raw, dir string) {
	t.Helper()
	a := newAtelier(t, raw, dir, false)
	if err := a.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.cases.Put(t.Context(), atelierCase{ID: "c-ann", Email: "ann@atelier.test", Note: "a note about ann", Stage: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.export.Dispatch(t.Context(), atelierWho{ID: "ann@atelier.test"}); err != nil {
		t.Fatal(err)
	}
	if err := a.app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAFormerJournalOnMySQLIsClosedAsItWasAndContinued(t *testing.T) {
	raw := fresh(t)
	dir := t.TempDir()
	a := newAtelier(t, raw, dir, false)
	if err := a.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.app.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	former := writeFormerJournal(t, raw, 3)
	if status, out := verified(t, raw, dir); status != 0 || !strings.HasSuffix(out, "the chain holds: 3 entries\n") || strings.Contains(out, "closed") {
		t.Fatalf("the former journal alone: %d\n%s", status, out)
	}

	for range 2 {
		exported(t, raw, dir)
	}
	docs := journalOn(t, raw)
	for key, doc := range former {
		if !bytes.Equal(docs[key], doc) {
			t.Errorf("former entry %s changed:\n got %s\nwant %s", key, docs[key], doc)
		}
	}
	var lastHash struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(former[fmt.Sprintf("%020d", 3)], &lastHash); err != nil {
		t.Fatal(err)
	}
	continues := 0
	for i := 4; i <= len(docs); i++ {
		var e map[string]any
		if err := json.Unmarshal(docs[fmt.Sprintf("%020d", i)], &e); err != nil {
			t.Fatal(err)
		}
		if e["refs"] != "app/v1" {
			t.Errorf("entry %d is not under the app/v1 labels: %v", i, e)
		}
		if e["op"] == "continue" {
			continues++
			if i != 4 || e["follows"] != lastHash.Hash {
				t.Errorf("entry %d continues %v, want entry 4 to name %s", i, e["follows"], lastHash.Hash)
			}
		}
	}
	if continues != 1 || len(docs) != 6 {
		t.Errorf("%d entries, %d of them continuing a journal: want 6 and 1", len(docs), continues)
	}
	status, out := verified(t, raw, dir)
	if status != 0 || !strings.Contains(out, "a journal closed at entry 3: entry 4 names its hash and continues it\n") || !strings.HasSuffix(out, "the chain holds: 6 entries\n") {
		t.Errorf("privacy journal -verify once closed: %d\n%s", status, out)
	}

	changed := bytes.Replace(former[fmt.Sprintf("%020d", 2)], []byte(`"by":"atelier/command/export"`), []byte(`"by":"studio"`), 1)
	if _, err := open(t, raw).ExecContext(t.Context(), `UPDATE app_privacy__journal SET doc = ? WHERE doc_key = ?`, changed, []byte(fmt.Sprintf("%020d", 2))); err != nil {
		t.Fatal(err)
	}
	if status, out := verified(t, raw, dir); status == 0 || !strings.Contains(out, "the chain breaks at entry 2") {
		t.Errorf("privacy journal -verify with a former entry changed: %d\n%s", status, out)
	}
}
