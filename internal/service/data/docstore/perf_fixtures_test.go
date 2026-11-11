//go:build !race

// The fixture of docstore's performance contract (design/sdk.yaml, budgets):
// it sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package docstore_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/docstore"
)

// perfGetIntoErr keeps each call's result alive.
var perfGetIntoErr error

// perfGetInto is GetInto's fixture (#813): a memory store of a thousand
// accounts, read in turn into one value the caller keeps, so every call
// decodes a document whose strings differ from the last one's — what a read
// loop over a store costs, not a decoder handed the same bytes twice.
func perfGetInto(tb testing.TB) func() {
	store, err := docstore.Open(docstore.Config[account]{Key: accountKey}, accountIndexes()...)
	if err != nil {
		tb.Fatalf("Open() = %v", err)
	}
	keys := make([]string, 1_000)
	for i := range keys {
		doc := benchDoc(i)
		if putErr := store.Put(doc); putErr != nil {
			tb.Fatalf("Put() = %v", putErr)
		}
		keys[i] = doc.ID
	}
	var dst account
	i := 0
	return func() {
		perfGetIntoErr = store.GetInto(keys[i%len(keys)], &dst)
		i++
	}
}
