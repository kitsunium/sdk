package secret_test

import (
	"bytes"
	"testing"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// TestMemorySubjectKeyStoreKeepsThePortsContract walks the memory store
// through every answer the port defines: absent and taken are answers, a
// Replace needs the exact current bytes and never brings back a deleted key,
// a Delete of nothing is not an error.
func TestMemorySubjectKeyStoreKeepsThePortsContract(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemorySubjectKeyStore()
	ctx := t.Context()
	if held, found, err := store.Get(ctx, "user:1"); held != nil || found || err != nil {
		t.Fatalf("Get of nothing = (%x, %v, %v), want (nil, false, nil)", held, found, err)
	}
	if inserted, err := store.Insert(ctx, "user:1", []byte("first")); !inserted || err != nil {
		t.Fatalf("Insert = (%v, %v), want (true, nil)", inserted, err)
	}
	if inserted, err := store.Insert(ctx, "user:1", []byte("second")); inserted || err != nil {
		t.Fatalf("Insert over a key = (%v, %v), want (false, nil)", inserted, err)
	}
	if replaced, err := store.Replace(ctx, "user:1", []byte("stale"), []byte("next")); replaced || err != nil {
		t.Fatalf("Replace from stale bytes = (%v, %v), want (false, nil)", replaced, err)
	}
	if replaced, err := store.Replace(ctx, "user:1", []byte("first"), []byte("next")); !replaced || err != nil {
		t.Fatalf("Replace from the current bytes = (%v, %v), want (true, nil)", replaced, err)
	}
	if held, found, err := store.Get(ctx, "user:1"); string(held) != "next" || !found || err != nil {
		t.Fatalf("Get = (%q, %v, %v), want the replaced key", held, found, err)
	}
	if deleted, err := store.Delete(ctx, "user:1"); !deleted || err != nil {
		t.Fatalf("Delete = (%v, %v), want (true, nil)", deleted, err)
	}
	if deleted, err := store.Delete(ctx, "user:1"); deleted || err != nil {
		t.Fatalf("Delete of nothing = (%v, %v), want (false, nil)", deleted, err)
	}
	if replaced, err := store.Replace(ctx, "user:1", []byte("next"), []byte("back")); replaced || err != nil {
		t.Fatalf("Replace of a deleted key = (%v, %v), want (false, nil): never resurrect", replaced, err)
	}
	if _, found, err := store.Get(ctx, "user:1"); found || err != nil {
		t.Fatalf("Get after the refused Replace = (%v, %v): a deleted key came back", found, err)
	}
}

// TestMemorySubjectKeyStoreSharesNoSlice pins the copies: a slice handed in
// may be reused by the caller, and a slice handed out may be written by it,
// without either reaching the store.
func TestMemorySubjectKeyStoreSharesNoSlice(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemorySubjectKeyStore()
	ctx := t.Context()
	given := []byte("wrapped")
	if _, err := store.Insert(ctx, "user:1", given); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	given[0] = 'X'
	got, _, err := store.Get(ctx, "user:1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got[1] = 'Y'
	for entry := range store.All(ctx) {
		entry.Wrapped[2] = 'Z'
	}
	if held, _, heldErr := store.Get(ctx, "user:1"); heldErr != nil || !bytes.Equal(held, []byte("wrapped")) {
		t.Fatalf("the store holds (%q, %v), want the bytes it was given", held, heldErr)
	}
}

// TestMemorySubjectKeyStoreAllIsSortedAndStoppable pins All: every key once,
// in subject order, stopping when the caller stops, and writable while it
// ranges — the re-wrap replaces keys between two yields.
func TestMemorySubjectKeyStoreAllIsSortedAndStoppable(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemorySubjectKeyStore()
	ctx := t.Context()
	for _, subject := range []string{"user:3", "user:1", "user:2"} {
		if _, err := store.Insert(ctx, subject, []byte(subject)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	var seen []string
	for entry, err := range store.All(ctx) {
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		seen = append(seen, entry.Subject)
		//: a write while ranging must not deadlock.
		if _, replaceErr := store.Replace(ctx, entry.Subject, entry.Wrapped, []byte("moved")); replaceErr != nil {
			t.Fatalf("Replace while ranging: %v", replaceErr)
		}
	}
	if want := []string{"user:1", "user:2", "user:3"}; len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("All yielded %v, want %v", seen, want)
	}
	count := 0
	for range store.All(ctx) {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("All yielded %d keys after the caller stopped at one", count)
	}
}

// TestMemorySubjectKeyStoreRefusesAMalformedSubject pins that the reference
// store validates on its own, as every secret store does, and says nothing
// about the string it refused.
func TestMemorySubjectKeyStoreRefusesAMalformedSubject(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemorySubjectKeyStore()
	ctx := t.Context()
	const bad = "Jane@Example"
	_, _, getErr := store.Get(ctx, bad)
	_, insertErr := store.Insert(ctx, bad, []byte("x"))
	_, replaceErr := store.Replace(ctx, bad, nil, []byte("x"))
	_, deleteErr := store.Delete(ctx, bad)
	for _, err := range []error{getErr, insertErr, replaceErr, deleteErr} {
		if !errs.HasCode(err, coresecret.CodeInvalidSubject) {
			t.Fatalf("verdict = %v, want InvalidSubject", err)
		}
	}
}
