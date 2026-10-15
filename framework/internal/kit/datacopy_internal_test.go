package kit

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A data copy's documents and fingerprints (ADR 0178 C7), between two of the
// SDK's document stores in memory: every document with its versions — their
// numbers, instants and metadata — and the trash at its instants comes
// across, both sides' fingerprints are equal, and a difference injected on
// the target's side is caught and the first key that differs named.

// page is what the copy's stores keep.
type page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// memorySink is a document store in memory, written as a copy writes a
// table: through PutDocument, which the file engine has too.
type memorySink struct{ *docEngine[page] }

func (m memorySink) putDocument(_ context.Context, key string, versions []docstore.Version, trashedAt time.Time) error {
	return m.ds.PutDocument(key, versions, trashedAt)
}

func (m memorySink) insertRaw(_ context.Context, raw json.RawMessage) error {
	var p page
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	return m.ds.Insert(p)
}

// pages opens a store of pages in memory, keeping two former versions on
// clk.
func pages(t *testing.T, clk clock.Clock) *docEngine[page] {
	t.Helper()
	e, err := openDocEngine(func(p page) string { return p.ID }, storeFS{}, nil, versionsOn{keep: 2, clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// history writes pages with versions — renamed, stamped — and trashes one.
func history(t *testing.T, e *docEngine[page], clk *clock.ManualClock) {
	t.Helper()
	for _, title := range []string{"draft", "review", "final"} {
		if err := e.ds.PutStamped(page{ID: "p1", Title: title}, docstore.Stamp{Meta: map[string]string{"by": title}}); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Minute)
	}
	if err := e.ds.Put(page{ID: "p2", Title: "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := e.ds.Put(page{ID: "p3", Title: "trashed"}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := e.ds.Trash("p3"); err != nil {
		t.Fatal(err)
	}
}

// TestACopyCarriesVersionsAndTheTrashAndItsFingerprintsMatch copies a store
// with versions and a trashed document, and finds both sides' fingerprints
// equal, the trash at its instant, the versions under their numbers.
func TestACopyCarriesVersionsAndTheTrashAndItsFingerprintsMatch(t *testing.T) {
	clk := clock.NewManualClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	from := pages(t, clk)
	history(t, from, clk)
	to := memorySink{pages(t, clock.NewManualClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))}
	part := copyPart{name: "site__pages", from: from, to: to}
	n, err := copyDocuments(t.Context(), part)
	if err != nil || n != 3 {
		t.Fatalf("copyDocuments() = %d, %v; want the three records", n, err)
	}
	if err := verifyDocuments(t.Context(), part); err != nil {
		t.Fatalf("verifyDocuments() = %v, want equal fingerprints", err)
	}
	tombs, err := to.ds.Tombstones()
	if err != nil || len(tombs) != 1 || tombs[0].Key != "p3" || !tombs[0].At.Equal(time.Date(2026, 10, 1, 10, 3, 0, 0, time.UTC)) {
		t.Fatalf("the target's trash = %+v, %v", tombs, err)
	}
	versions, err := to.ds.Versions("p1")
	if err != nil || len(versions) != 3 || versions[0].Number != 3 || versions[2].Number != 1 || versions[0].Meta["by"] != "final" {
		t.Fatalf("the target's versions of p1 = %+v, %v", versions, err)
	}
}

// TestADivergenceIsCaughtAndItsFirstKeyNamed alters the target once copied
// — a version renumbered, a document changed, a record the source does not
// hold — and finds DATA_COPY_DIVERGED naming the first key that differs.
func TestADivergenceIsCaughtAndItsFirstKeyNamed(t *testing.T) {
	cases := []struct {
		alter func(t *testing.T, to memorySink)
		key   string
	}{
		{key: "p2", alter: func(t *testing.T, to memorySink) {
			if err := to.ds.Put(page{ID: "p2", Title: "tampered"}); err != nil {
				t.Fatal(err)
			}
		}},
		{key: "p0", alter: func(t *testing.T, to memorySink) {
			if err := to.ds.Put(page{ID: "p0", Title: "extra"}); err != nil {
				t.Fatal(err)
			}
		}},
		{key: "p3", alter: func(t *testing.T, to memorySink) {
			if err := to.ds.Purge("p3"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			clk := clock.NewManualClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
			from := pages(t, clk)
			history(t, from, clk)
			to := memorySink{pages(t, clk)}
			part := copyPart{name: "site__pages", from: from, to: to}
			if _, err := copyDocuments(t.Context(), part); err != nil {
				t.Fatal(err)
			}
			c.alter(t, to)
			err := verifyDocuments(t.Context(), part)
			if !errs.HasCode(err, CodeDataCopyDiverged) {
				t.Fatalf("verifyDocuments() = %v, want DATA_COPY_DIVERGED", err)
			}
			if got := fieldOf(err, "key"); got != c.key {
				t.Fatalf("the divergence named %q, want %q", got, c.key)
			}
		})
	}
}

// TestAVersionRenumberedChangesItsFingerprint pins the fingerprint's parts:
// a version under another number, at another instant, with other metadata,
// is another fingerprint; the whitespace of a snapshot is not.
func TestAVersionRenumberedChangesItsFingerprint(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	v := docstore.Version{Number: 2, At: at, Meta: map[string]string{"by": "ada"}, JSON: json.RawMessage(`{"id":"p1"}`)}
	base := mustVersionDigest(t, &v)
	for name, other := range map[string]docstore.Version{
		"number":   {Number: 3, At: at, Meta: v.Meta, JSON: v.JSON},
		"instant":  {Number: 2, At: at.Add(time.Nanosecond), Meta: v.Meta, JSON: v.JSON},
		"metadata": {Number: 2, At: at, Meta: map[string]string{"by": "bob"}, JSON: v.JSON},
		"document": {Number: 2, At: at, Meta: v.Meta, JSON: json.RawMessage(`{"id":"p2"}`)},
	} {
		if mustVersionDigest(t, &other) == base {
			t.Errorf("a version with another %s has the same fingerprint", name)
		}
	}
	spaced := docstore.Version{Number: 2, At: at, Meta: v.Meta, JSON: json.RawMessage("{ \"id\": \"p1\" }")}
	if mustVersionDigest(t, &spaced) != base {
		t.Error("the whitespace of a snapshot changed a version's fingerprint")
	}
	if messageDigest([]byte("m"), false, 2, "", 0) == messageDigest([]byte("m"), false, 1, "", 0) {
		t.Error("a message's delivery count is not in its fingerprint")
	}
}

// mustVersionDigest is v's fingerprint in the table t, under the key p1.
func mustVersionDigest(t *testing.T, v *docstore.Version) digest {
	t.Helper()
	sum, err := versionDigest("t", "p1", v)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
