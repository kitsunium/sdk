package docstore_test

import (
	"context"
	"slices"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// doc is the document type the doubles below store.
type doc struct{ ID string }

// collectionDouble carries EXACTLY the methods Collection declares, Announcer's
// included, and nothing else. It exists to fail compilation the day a method is
// added to the port: pkg/v1/data/docstore aliases it, Go interfaces are
// structural, and every double a consumer wrote against it would stop
// compiling with no deprecation window (ADR 0039). A capability grows as a
// sibling, as Versioned did.
type collectionDouble struct{}

func (collectionDouble) OnWrite(func(string)) func()                    { return func() {} }
func (collectionDouble) OnDelete(func(string)) func()                   { return func() {} }
func (collectionDouble) Get(string) (doc, error)                        { return doc{}, nil }
func (collectionDouble) List() ([]doc, error)                           { return nil, nil }
func (collectionDouble) Filter(func(doc) bool) ([]doc, error)           { return nil, nil }
func (collectionDouble) Entries(int) ([]coredocstore.EntryValue, error) { return nil, nil }
func (collectionDouble) Lookup(string, string) (doc, error)             { return doc{}, nil }
func (collectionDouble) Find(string, string) ([]doc, error)             { return nil, nil }
func (collectionDouble) Put(doc) error                                  { return nil }
func (collectionDouble) Insert(doc) error                               { return nil }
func (collectionDouble) Replace(doc) error                              { return nil }
func (collectionDouble) Update(string, func(*doc) error) (doc, error)   { return doc{}, nil }
func (collectionDouble) Delete(string) error                            { return nil }

// versionedDouble carries exactly the methods Versioned declares.
type versionedDouble struct{}

func (versionedDouble) PutStamped(doc, coredocstore.StampValue) error     { return nil }
func (versionedDouble) InsertStamped(doc, coredocstore.StampValue) error  { return nil }
func (versionedDouble) ReplaceStamped(doc, coredocstore.StampValue) error { return nil }
func (versionedDouble) UpdateStamped(string, coredocstore.StampValue, func(*doc) error) (doc, error) {
	return doc{}, nil
}
func (versionedDouble) Versions(string) ([]coredocstore.VersionValue, error) { return nil, nil }
func (versionedDouble) Version(string, uint64) (coredocstore.VersionValue, error) {
	return coredocstore.VersionValue{}, nil
}

func (versionedDouble) RewriteVersions(string, func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) error {
	return nil
}

// contextDouble carries exactly the methods CollectionContext declares.
type contextDouble struct{}

func (contextDouble) OnWrite(func(string)) func()                           { return func() {} }
func (contextDouble) OnDelete(func(string)) func()                          { return func() {} }
func (contextDouble) Get(context.Context, string) (doc, error)              { return doc{}, nil }
func (contextDouble) List(context.Context) ([]doc, error)                   { return nil, nil }
func (contextDouble) Filter(context.Context, func(doc) bool) ([]doc, error) { return nil, nil }
func (contextDouble) Lookup(context.Context, string, string) (doc, error)   { return doc{}, nil }
func (contextDouble) Find(context.Context, string, string) ([]doc, error)   { return nil, nil }
func (contextDouble) Put(context.Context, doc) error                        { return nil }
func (contextDouble) Insert(context.Context, doc) error                     { return nil }
func (contextDouble) Replace(context.Context, doc) error                    { return nil }
func (contextDouble) Delete(context.Context, string) error                  { return nil }
func (contextDouble) Update(context.Context, string, func(*doc) error) (doc, error) {
	return doc{}, nil
}

func (contextDouble) Entries(context.Context, int) ([]coredocstore.EntryValue, error) {
	return nil, nil
}

// versionedContextDouble carries exactly the methods VersionedContext declares.
type versionedContextDouble struct{}

func (versionedContextDouble) PutStamped(context.Context, doc, coredocstore.StampValue) error {
	return nil
}

func (versionedContextDouble) InsertStamped(context.Context, doc, coredocstore.StampValue) error {
	return nil
}

func (versionedContextDouble) ReplaceStamped(context.Context, doc, coredocstore.StampValue) error {
	return nil
}

func (versionedContextDouble) UpdateStamped(context.Context, string, coredocstore.StampValue, func(*doc) error) (doc, error) {
	return doc{}, nil
}

func (versionedContextDouble) Versions(context.Context, string) ([]coredocstore.VersionValue, error) {
	return nil, nil
}

func (versionedContextDouble) Version(context.Context, string, uint64) (coredocstore.VersionValue, error) {
	return coredocstore.VersionValue{}, nil
}

func (versionedContextDouble) RewriteVersions(context.Context, string, func([]coredocstore.VersionValue) ([]coredocstore.VersionValue, error)) error {
	return nil
}

// TestDoublesStillSatisfyThePorts is the ADR 0039 guard, executable rather
// than documentary: each double above holds exactly its port's methods, so a
// method added to a port fails this file's compilation before review.
func TestDoublesStillSatisfyThePorts(t *testing.T) {
	t.Parallel()
	var collection coredocstore.Collection[doc] = collectionDouble{}
	var versioned coredocstore.Versioned[doc] = versionedDouble{}
	var withContext coredocstore.CollectionContext[doc] = contextDouble{}
	var versionedWithContext coredocstore.VersionedContext[doc] = versionedContextDouble{}
	announcers := []coredocstore.Announcer{collection, withContext}
	if _, err := collection.Get("k"); err != nil {
		t.Fatalf("Collection.Get = %v, want nil", err)
	}
	if _, err := versioned.Versions("k"); err != nil {
		t.Fatalf("Versioned.Versions = %v, want nil", err)
	}
	if _, err := withContext.Get(t.Context(), "k"); err != nil {
		t.Fatalf("CollectionContext.Get = %v, want nil", err)
	}
	if _, err := versionedWithContext.Versions(t.Context(), "k"); err != nil {
		t.Fatalf("VersionedContext.Versions = %v, want nil", err)
	}
	for _, announcer := range announcers {
		announcer.OnWrite(func(string) {})()
	}
}

// TestSentinelsCarryTheirDocumentedCodes pins every sentinel's code, reason,
// exit status and HTTP status. The statuses are integers since ADR 0160 moved
// the declarations out of the engine, which imported net/http for them; this
// is what proves the move changed none of them.
func TestSentinelsCarryTheirDocumentedCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err    *errs.Error
		reason string
		code   errs.Code
		exit   int
		status int
	}{
		{coredocstore.DocumentNotFound, "DOCUMENT_NOT_FOUND", 0x00_03_50_01, 70, 404},
		{coredocstore.DocumentExists, "DOCUMENT_EXISTS", 0x00_03_50_02, 70, 409},
		{coredocstore.UniqueKeyTaken, "UNIQUE_KEY_TAKEN", 0x00_03_50_03, 70, 409},
		{coredocstore.DocumentKeyEmpty, "DOCUMENT_KEY_EMPTY", 0x00_03_50_04, 70, 400},
		{coredocstore.DocumentKeyChanged, "DOCUMENT_KEY_CHANGED", 0x00_03_50_05, 70, 400},
		{coredocstore.IndexUnknown, "INDEX_UNKNOWN", 0x00_03_50_06, 70, 400},
		{coredocstore.IndexNotUnique, "INDEX_NOT_UNIQUE", 0x00_03_50_07, 70, 400},
		{coredocstore.DocumentUndecodable, "DOCUMENT_UNDECODABLE", 0x00_03_50_08, 65, 500},
		{coredocstore.DocumentUnencodable, "DOCUMENT_UNENCODABLE", 0x00_03_50_09, 70, 500},
		{coredocstore.PersistFailed, "PERSIST_FAILED", 0x00_03_50_0A, 74, 500},
		{coredocstore.LoadFailed, "LOAD_FAILED", 0x00_03_50_0B, 65, 500},
		{coredocstore.IndexBroken, "INDEX_BROKEN", 0x00_03_50_0C, 65, 500},
		{coredocstore.StoreClosed, "STORE_CLOSED", 0x00_03_50_0D, 70, 503},
		{coredocstore.StoreMisconfigured, "STORE_MISCONFIGURED", 0x00_03_50_0E, 78, 500},
		{coredocstore.WriteUnconfirmed, "WRITE_UNCONFIRMED", 0x00_03_50_0F, 74, 500},
		{coredocstore.StatementFailed, "STATEMENT_FAILED", 0x00_03_50_10, 75, 503},
		{coredocstore.KeyTooLong, "KEY_TOO_LONG", 0x00_03_50_11, 70, 400},
		{coredocstore.VersionsNotKept, "VERSIONS_NOT_KEPT", 0x00_03_50_12, 70, 400},
		{coredocstore.VersionNotFound, "VERSION_NOT_FOUND", 0x00_03_50_13, 70, 404},
		{coredocstore.VersionsRewriteRefused, "VERSIONS_REWRITE_REFUSED", 0x00_03_50_14, 70, 400},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			if code, ok := errs.CodeOf(tc.err); !ok || code != tc.code {
				t.Errorf("CodeOf = (%v, %t), want (%v, true)", code, ok, tc.code)
			}
			if !errs.HasReason(tc.err, tc.reason) {
				t.Errorf("reason is not %q", tc.reason)
			}
			if got := errs.ExitCodeOf(tc.err); got != tc.exit {
				t.Errorf("ExitCodeOf = %d, want %d", got, tc.exit)
			}
			if got := errs.HTTPStatusOf(tc.err); got != tc.status {
				t.Errorf("HTTPStatusOf = %d, want %d", got, tc.status)
			}
		})
	}
}

// TestUniqueFilesOneKeyOrNone pins the declaration both engines file by: a
// unique index gives a document its one key, an empty key is no key, and a nil
// function stays nil for the engine's constructor to refuse by name rather
// than panic on.
func TestUniqueFilesOneKeyOrNone(t *testing.T) {
	t.Parallel()
	spec := coredocstore.Unique("id", func(d doc) string { return d.ID })
	if !spec.Unique || spec.Name != "id" {
		t.Fatalf("Unique = {Name: %q, Unique: %t}, want {id, true}", spec.Name, spec.Unique)
	}
	if got := spec.Keys(doc{ID: "a"}); !slices.Equal(got, []string{"a"}) {
		t.Errorf("Keys(a) = %q, want [a]", got)
	}
	if got := spec.Keys(doc{}); got != nil {
		t.Errorf("Keys(empty) = %q, want nil: the empty key is not a key", got)
	}
	if nilKey := coredocstore.Unique[doc]("id", nil); nilKey.Keys != nil || !nilKey.Unique {
		t.Errorf("Unique(nil) = {Keys set: %t, Unique: %t}, want no Keys and Unique", nilKey.Keys != nil, nilKey.Unique)
	}
	multi := coredocstore.Index("teams", func(doc) []string { return []string{"red", "blue"} })
	if multi.Unique || multi.Name != "teams" || !slices.Equal(multi.Keys(doc{}), []string{"red", "blue"}) {
		t.Errorf("Index = {Name: %q, Unique: %t}, want {teams, false} with its keys as given", multi.Name, multi.Unique)
	}
}
