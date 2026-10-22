package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A data copy's documents and fingerprints (ADR 0178 C7), between two of the
// store subsystem's document stores in memory: every document with its
// versions — their numbers, instants and metadata — and the trash at its
// instants comes across, both sides' fingerprints are equal, and a difference
// injected on the target's side is caught and the first key that differs
// named.

// page is what the copy's stores keep.
type page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// byKey carries who a test's write is by, which the pages' stamp reads.
type byKey struct{}

// pageEngine is a store of pages in memory, as a copy reads and writes one:
// its engine, its documents, its trash and its versions.
type pageEngine interface {
	plug.StoreEngine
	plug.Trasher
	plug.VersionKeeper
	copySink
}

// pages opens a store of pages in memory, keeping two former versions on
// clk, each stamped by whom its context says.
func pages(t *testing.T, clk clock.Clock) pageEngine {
	t.Helper()
	eng, err := openEngine(plug.StoreConfig{
		Name: "pages",
		Key: func(d plug.DocumentValue) string {
			p, _ := d.Value.(page)
			return p.ID
		},
		Decode: func(raw json.RawMessage) (any, error) {
			var p page
			err := json.Unmarshal(raw, &p)
			return p, err
		},
		Versions: 2, Clock: clk.Now,
		Stamp: func(ctx context.Context) plug.StampValue {
			if by, ok := ctx.Value(byKey{}).(string); ok {
				return plug.StampValue{Meta: map[string]string{"by": by}}
			}
			return plug.StampValue{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	pe, ok := eng.(pageEngine)
	if !ok {
		t.Fatalf("the store subsystem's engine in memory is not a document store a copy writes: %T", eng)
	}
	return pe
}

// putPage writes p into e, by whom ctx says.
func putPage(ctx context.Context, t *testing.T, e pageEngine, p page) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Write(ctx, plug.DocumentValue{JSON: raw, Value: p}, plug.WriteUpsert); err != nil {
		t.Fatal(err)
	}
}

// history writes pages with versions — renamed, stamped — and trashes one.
func history(t *testing.T, e pageEngine, clk *clock.ManualClock) {
	t.Helper()
	for _, title := range []string{"draft", "review", "final"} {
		putPage(context.WithValue(t.Context(), byKey{}, title), t, e, page{ID: "p1", Title: title})
		clk.Advance(time.Minute)
	}
	putPage(t.Context(), t, e, page{ID: "p2", Title: "kept"})
	putPage(t.Context(), t, e, page{ID: "p3", Title: "trashed"})
	clk.Advance(time.Hour)
	if err := e.Trash(t.Context(), "p3"); err != nil {
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
	to := pages(t, clock.NewManualClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
	part := copyPart{name: "site__pages", from: from, to: to}
	n, err := copyDocuments(t.Context(), part)
	if err != nil || n != 3 {
		t.Fatalf("copyDocuments() = %d, %v; want the three records", n, err)
	}
	if err := verifyDocuments(t.Context(), part); err != nil {
		t.Fatalf("verifyDocuments() = %v, want equal fingerprints", err)
	}
	tombs, err := to.Tombstones(t.Context())
	if err != nil || len(tombs) != 1 || tombs[0].Key != "p3" || !tombs[0].At.Equal(time.Date(2026, 10, 1, 10, 3, 0, 0, time.UTC)) {
		t.Fatalf("the target's trash = %+v, %v", tombs, err)
	}
	versions, err := to.StoredVersions(t.Context(), "p1")
	if err != nil || len(versions) != 3 || versions[0].Number != 3 || versions[2].Number != 1 || versions[0].Meta["by"] != "final" {
		t.Fatalf("the target's versions of p1 = %+v, %v", versions, err)
	}
}

// TestADivergenceIsCaughtAndItsFirstKeyNamed alters the target once copied
// — a version renumbered, a document changed, a record the source does not
// hold — and finds DATA_COPY_DIVERGED naming the first key that differs.
func TestADivergenceIsCaughtAndItsFirstKeyNamed(t *testing.T) {
	cases := []struct {
		alter func(t *testing.T, to pageEngine)
		key   string
	}{
		{key: "p2", alter: func(t *testing.T, to pageEngine) {
			putPage(t.Context(), t, to, page{ID: "p2", Title: "tampered"})
		}},
		{key: "p0", alter: func(t *testing.T, to pageEngine) {
			putPage(t.Context(), t, to, page{ID: "p0", Title: "extra"})
		}},
		{key: "p3", alter: func(t *testing.T, to pageEngine) {
			if err := to.Purge(t.Context(), "p3"); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			clk := clock.NewManualClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
			from := pages(t, clk)
			history(t, from, clk)
			to := pages(t, clk)
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
	v := plug.VersionValue{Number: 2, At: at, Meta: map[string]string{"by": "ada"}, JSON: json.RawMessage(`{"id":"p1"}`)}
	base := mustVersionDigest(t, &v)
	for name, other := range map[string]plug.VersionValue{
		"number":   {Number: 3, At: at, Meta: v.Meta, JSON: v.JSON},
		"instant":  {Number: 2, At: at.Add(time.Nanosecond), Meta: v.Meta, JSON: v.JSON},
		"metadata": {Number: 2, At: at, Meta: map[string]string{"by": "bob"}, JSON: v.JSON},
		"document": {Number: 2, At: at, Meta: v.Meta, JSON: json.RawMessage(`{"id":"p2"}`)},
	} {
		if mustVersionDigest(t, &other) == base {
			t.Errorf("a version with another %s has the same fingerprint", name)
		}
	}
	spaced := plug.VersionValue{Number: 2, At: at, Meta: v.Meta, JSON: json.RawMessage("{ \"id\": \"p1\" }")}
	if mustVersionDigest(t, &spaced) != base {
		t.Error("the whitespace of a snapshot changed a version's fingerprint")
	}
	trailed, bare := &plug.DeadLetterValue{Reason: "R", Code: 5, Trail: []uint32{7}}, &plug.DeadLetterValue{Reason: "R", Code: 5}
	if messageDigest([]byte("m"), 1, trailed) == messageDigest([]byte("m"), 1, bare) {
		t.Error("a dead letter's trail does not change its fingerprint")
	}
	if messageDigest([]byte("m"), 1, bare) == messageDigest([]byte("m"), 1, nil) {
		t.Error("a dead letter and a live message share a fingerprint")
	}
	if messageDigest([]byte("m"), 2, nil) == messageDigest([]byte("m"), 1, nil) {
		t.Error("a message's delivery count is not in its fingerprint")
	}
}

// mustVersionDigest is v's fingerprint in the table t, under the key p1.
func mustVersionDigest(t *testing.T, v *plug.VersionValue) digest {
	t.Helper()
	sum, err := versionDigest("t", "p1", v)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
