package s3

import (
	"testing"

	svcdocstore "github.com/kitsunium/sdk/internal/service/data/docstore"
)

// note is the document the docstore rows keep.
type note struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// rowDocstoreOverS3 opens the docstore's file engine over the S3 engine and
// runs its write, read, version and reopen cycle: the overlay entries and the
// snapshot it folds them into are objects, listed as a directory (ADR 0110).
func rowDocstoreOverS3(t *testing.T, build fsFactory) {
	filesystem := build(t)
	open := func() *svcdocstore.Store[note] {
		store, err := svcdocstore.Open(svcdocstore.Config[note]{
			Key: func(n note) string { return n.ID }, FS: filesystem, Path: "notes.json", Versions: 4, FoldAt: 3,
		})
		if err != nil {
			t.Fatalf("docstore.Open = %v, want nil", err)
		}
		return store
	}
	store := open()
	for _, body := range []string{"one", "two", "three", "four"} {
		if err := store.Put(note{ID: "n1", Body: body}); err != nil {
			t.Fatalf("Put(%s) = %v, want nil", body, err)
		}
	}
	if err := store.Put(note{ID: "n2", Body: "other"}); err != nil {
		t.Fatalf("Put(n2) = %v, want nil", err)
	}
	versions, err := store.Versions("n1")
	if err != nil || len(versions) == 0 {
		t.Fatalf("Versions = (%d, %v), want the former bodies", len(versions), err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	reopened := open()
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Get("n1")
	if err != nil || got.Body != "four" {
		t.Fatalf("Get after reopen = (%+v, %v), want the last body", got, err)
	}
	all, err := reopened.List()
	if err != nil || len(all) != 2 {
		t.Fatalf("List after reopen = (%d, %v), want 2", len(all), err)
	}
}

func TestTheDocstoreFileEngineRunsOverTheFakeBucket(t *testing.T) {
	t.Parallel()
	rowDocstoreOverS3(t, fakeFS)
}
