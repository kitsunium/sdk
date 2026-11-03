// Package docstore_test — the file engine's loader fuzzed over the three
// formats an operator can edit and a crash can truncate: the snapshot, the
// versions file and an overlay entry (#1061, ADR 0110, ADR 0143).
//
// The invariant every target holds: Open never panics; it opens, or refuses
// with exactly one of LOAD_FAILED, INDEX_BROKEN and DOCUMENT_UNDECODABLE, whose
// rendering and fields — the file field excepted, which names the store's own
// files — carry no key and no document byte; and a store that opened folds to
// files that reopen with the same Entries, Versions and Tombstones.
package docstore_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
)

// fuzzVersions is what the fuzzed stores keep: two former versions, so the
// versions file and the entries' versions are read.
const fuzzVersions = 2

// fuzzConfig is the accounts store's configuration over fsys for a target:
// versions kept, no automatic fold, a fixed clock.
func fuzzConfig(fsys corevfs.FullFS, versions int) docstore.Config[account] {
	cfg := versionedConfig(fsys, versions, clock.NewManualClock(epoch))
	cfg.FoldAt = -1
	return cfg
}

// storeFiles writes a real store's files — a plain, a versioned, a trashed
// and a deleted document — and returns the filesystem: folded when fold is
// set, abandoned with its overlay entries otherwise.
func storeFiles(tb testing.TB, fold bool) corevfs.FullFS {
	tb.Helper()
	fsys := svcvfs.NewMem()
	store, err := openWith(fuzzConfig(fsys, fuzzVersions))
	if err != nil {
		tb.Fatalf("Open = %v", err)
	}
	for _, step := range []func() error{
		func() error {
			return store.Insert(account{ID: "acc_plain", Email: "plain@example.com", Teams: []string{"core"}})
		},
		func() error { return store.Insert(account{ID: "acc_versioned", Email: "v1@example.com"}) },
		func() error { return store.Put(account{ID: "acc_versioned", Email: "v2@example.com", Name: "Ada"}) },
		func() error {
			_, err := store.UpdateStamped("acc_versioned", coredocstore.StampValue{Meta: map[string]string{"by": "ops"}},
				func(a *account) error { a.Name = "Grace"; return nil })
			return err
		},
		func() error { return store.Insert(account{ID: "acc_trashed", Email: "gone@example.com"}) },
		func() error { return store.Trash("acc_trashed") },
		func() error { return store.Insert(account{ID: "acc_deleted"}) },
		func() error { return store.Delete("acc_deleted") },
	} {
		if stepErr := step(); stepErr != nil {
			tb.Fatalf("writing the seed store: %v", stepErr)
		}
	}
	if fold {
		if closeErr := store.Close(); closeErr != nil {
			tb.Fatalf("Close = %v", closeErr)
		}
	}
	return fsys
}

// fileOf reads name from fsys, nil when absent.
func fileOf(fsys fs.FS, name string) []byte {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	return raw
}

// fuzzLoadCodes are the refusals the loader may answer with.
var fuzzLoadCodes = []errs.Code{
	coredocstore.CodeLoadFailed, coredocstore.CodeIndexBroken, coredocstore.CodeDocumentUndecodable,
}

// fieldNames are the JSON names of the files' records and of the account
// document: a refusal may name a field, and they are the program's.
var fieldNames = map[string]bool{
	"versions": true, "current": true, "former": true, "trashed": true, "number": true,
	"deleted": true, "doc": true, "key": true, "at": true, "meta": true,
	"id": true, "email": true, "name": true, "teams": true,
}

// secretsOf are the strings raw holds as JSON — every object key and every
// string value, at any depth, of six bytes or more and not a field name:
// what a refusal must never quote.
func secretsOf(raw []byte) []string {
	var tree any
	if json.Unmarshal(raw, &tree) != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for key, child := range node {
				out = append(out, key)
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		case string:
			out = append(out, node)
		}
	}
	walk(tree)
	return slices.DeleteFunc(out, func(s string) bool { return len(s) < 6 || fieldNames[s] })
}

// requireLoadInvariant opens cfg and holds the target's invariant, raw being
// the fuzzed bytes.
func requireLoadInvariant(t *testing.T, cfg docstore.Config[account], raw []byte) {
	t.Helper()
	store, err := openWith(cfg)
	if err != nil {
		matched := 0
		for _, code := range fuzzLoadCodes {
			if errs.HasCode(err, code) {
				matched++
			}
		}
		if matched != 1 {
			t.Fatalf("Open = %v, want exactly one of LOAD_FAILED, INDEX_BROKEN, DOCUMENT_UNDECODABLE", err)
		}
		rendered := []string{err.Error()}
		for _, field := range errs.FieldsOf(err) {
			if field.Key() != "file" {
				rendered = append(rendered, field.Key()+"="+field.StringValue())
			}
		}
		for _, secret := range secretsOf(raw) {
			for _, text := range rendered {
				if strings.Contains(text, secret) {
					t.Fatalf("Open refused with %q, which quotes %q from the file", text, secret)
				}
			}
		}
		return
	}
	before := storeState(t, store)
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("Close (the fold) = %v", closeErr)
	}
	again, reopenErr := openWith(cfg)
	if reopenErr != nil {
		t.Fatalf("Open after the fold = %v, want the store the first Open served", reopenErr)
	}
	defer func() { must(t, again.Close()) }()
	if after := storeState(t, again); after != before {
		t.Fatalf("after the fold the store holds\n%s\nwant\n%s", after, before)
	}
}

// storeState renders what a store serves — Entries, every key's Versions,
// Tombstones — so two opens compare as text, instants compared as instants.
func storeState(t *testing.T, store *docstore.Store[account]) string {
	t.Helper()
	var b strings.Builder
	entries, err := store.Entries(0)
	must(t, err)
	tombs, tombErr := store.Tombstones()
	if tombErr != nil && !errs.HasCode(tombErr, coredocstore.CodeVersionsNotKept) {
		t.Fatalf("Tombstones = %v", tombErr)
	}
	keys := make([]string, 0, len(entries)+len(tombs))
	for _, entry := range entries {
		//: canonical: the snapshot holds a document as an operator left it —
		//: indented, unescaped — and the fold writes it as json.Marshal does.
		doc := canonicalJSON(t, entry.JSON)
		fmt.Fprintf(&b, "doc %q %s\n", entry.Key, doc)
		keys = append(keys, entry.Key)
	}
	for _, tomb := range tombs {
		fmt.Fprintf(&b, "trashed %q %s\n", tomb.Key, tomb.At.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
		keys = append(keys, tomb.Key)
	}
	for _, key := range keys {
		versions, versionsErr := store.Versions(key)
		if versionsErr != nil {
			if errs.HasCode(versionsErr, coredocstore.CodeVersionsNotKept) {
				continue
			}
			t.Fatalf("Versions = %v", versionsErr)
		}
		for _, v := range versions {
			fmt.Fprintf(&b, "version %q %d %s %v %s\n", key, v.Number,
				v.At.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), v.Meta, canonicalJSON(t, v.JSON))
		}
	}
	return b.String()
}

// canonicalJSON renders raw as json.Marshal renders its decoding, so two
// spellings of one document compare equal.
func canonicalJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("Entries served a document that is not JSON: %v", err)
	}
	out, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("re-encoding a document: %v", err)
	}
	return string(out)
}

// FuzzOpenSnapshot opens a store over fuzzed bytes at its snapshot's path.
func FuzzOpenSnapshot(f *testing.F) {
	folded := storeFiles(f, true)
	f.Add(fileOf(folded, snapshotPath))
	for _, seed := range []string{
		`{}`, `null`, `[]`, `{"":{"id":""}}`, `{"acc_1":{"id":"acc_2"}}`,
		`{"acc_1":{"id":"acc_1","email":"a@example.com"},"acc_2":{"id":"acc_2","email":"a@example.com"}}`,
		`{"acc_1":{"id":5}}`, `{"acc_1":`, `{"acc_1":{"id":"acc_1","teams":"core"}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		fsys := svcvfs.NewMem()
		must(t, fsys.MkdirAll(path.Dir(snapshotPath), 0o700))
		must(t, fsys.WriteFile(snapshotPath, raw, 0o600))
		requireLoadInvariant(t, fuzzConfig(fsys, 0), raw)
	})
}

// FuzzOpenVersions opens a store keeping versions over a real folded
// snapshot and fuzzed bytes at its versions file.
func FuzzOpenVersions(f *testing.F) {
	folded := storeFiles(f, true)
	snapshot := fileOf(folded, snapshotPath)
	f.Add(fileOf(folded, snapshotPath+".versions"))
	for _, seed := range []string{
		`{}`, `null`, `{"":{"current":{"number":1}}}`, `{"acc_plain":null}`,
		`{"acc_plain":{"current":{"number":0}}}`,
		`{"acc_plain":{"current":{"number":2},"former":[{"number":2,"doc":{"id":"acc_plain"}}]}}`,
		`{"acc_plain":{"current":{"number":"x"}}}`,
		`{"acc_plain":{"current":{"number":1,"meta":{"by":5}}}}`,
		`{"acc_nobody":{"current":{"number":1}}}`,
		`{"acc_plain":{"current":{"number":1},"trashed":{"at":"2026-01-01T00:00:00Z","doc":{"id":"acc_plain"}}}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		fsys := svcvfs.NewMem()
		must(t, fsys.MkdirAll(path.Dir(snapshotPath), 0o700))
		must(t, fsys.WriteFile(snapshotPath, snapshot, 0o600))
		must(t, fsys.WriteFile(snapshotPath+".versions", raw, 0o600))
		requireLoadInvariant(t, fuzzConfig(fsys, fuzzVersions), raw)
	})
}

// FuzzReplayEntry opens a store keeping versions over a real folded store
// and one fuzzed overlay entry, under a valid entry name — the digest of
// acc_versioned, a key the store holds — or under the digest of another key.
func FuzzReplayEntry(f *testing.F) {
	folded := storeFiles(f, true)
	snapshot := fileOf(folded, snapshotPath)
	versions := fileOf(folded, snapshotPath+".versions")
	crashed := storeFiles(f, false)
	entries, err := fs.ReadDir(crashed, overlayPath)
	if err != nil {
		f.Fatalf("listing the seed overlay: %v", err)
	}
	for _, entry := range entries {
		f.Add(fileOf(crashed, path.Join(overlayPath, entry.Name())), true)
	}
	for _, seed := range []string{
		`{}`, `null`, `{"key":""}`, `{"key":"acc_versioned"}`,
		`{"key":"acc_versioned","doc":{"id":"acc_versioned"},"versions":{"current":{"number":0}}}`,
		`{"key":"acc_versioned","deleted":true,"versions":{"current":{"number":1}}}`,
		`{"key":"acc_versioned","doc":{"id":"acc_versioned"},"versions":{"current":{"number":1},"trashed":{"at":"2026-01-01T00:00:00Z","doc":{}}}}`,
		`{"key":"acc_versioned","doc":{"id":"acc_other"}}`,
		`{"key":"acc_versioned","doc":{"id":"acc_versioned","email":"plain@example.com"}}`,
	} {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	f.Fuzz(func(t *testing.T, raw []byte, valid bool) {
		fsys := svcvfs.NewMem()
		must(t, fsys.MkdirAll(overlayPath, 0o700))
		must(t, fsys.WriteFile(snapshotPath, snapshot, 0o600))
		must(t, fsys.WriteFile(snapshotPath+".versions", versions, 0o600))
		key := "acc_versioned"
		if !valid {
			key = "acc_elsewhere"
		}
		must(t, fsys.WriteFile(path.Join(overlayPath, entryNameOf(t, key)), raw, 0o600))
		requireLoadInvariant(t, fuzzConfig(fsys, fuzzVersions), raw)
	})
}
