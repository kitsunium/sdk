package kit_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
)

// A module with commands and queries (ADR 0005 in a module, ADR 0008): its
// ledger keeps entries, recorded by a command it exposes and summed by a
// query; the product mounts it under a prefix of its own.

var Ledgering = kit.NewService("entries", "Keeps a ledger's entries, for the tests.")

// LedgerEntry is one entry of the ledger.
type LedgerEntry struct {
	ID     string `json:"-" path:"id"`
	Amount int    `json:"amount"`
}

var LedgerEntries = Ledgering.Store("entries", func(e LedgerEntry) string { return e.ID })

var LedgerRecord = Ledgering.Command("record", LedgerRecordEntry).Expose("POST /entries/{id}")

var LedgerTotal = Ledgering.Query("total", LedgerSum)

var LedgerModule = kit.NewModule("ledger", "A ledger a product mounts, for the tests.", Ledgering)

// LedgerRecordEntry records an entry.
func LedgerRecordEntry(ctx context.Context, e LedgerEntry) (kit.EmptyValue, error) {
	return kit.EmptyValue{}, LedgerEntries.Put(ctx, e)
}

// LedgerSum sums the entries.
func LedgerSum(ctx context.Context, _ kit.EmptyValue) (int, error) {
	all, err := LedgerEntries.List(ctx)
	total := 0
	for _, e := range all {
		total += e.Amount
	}
	return total, err
}

// A command and a query a module's service declares are qualified like its
// other nodes; their exposure belongs to the module, is served under the
// mount's prefix and dispatches the qualified command.
func TestAModulesOperationsAreQualified(t *testing.T) {
	g := start(t, kit.Mount(LedgerModule, kit.Prefix("/books/"))).Graph()
	for _, id := range []string{"ledger.entries/command/record", "ledger.entries/query/total"} {
		if n := g.Node(id); n == nil || n.Module != "ledger" {
			t.Errorf("%s: %+v", id, n)
		}
	}
	type exposure struct{ module, path, exposes string }
	var got exposure
	if ep := g.Node("ledger.entries/endpoint/record"); ep != nil && ep.Endpoint != nil {
		got = exposure{ep.Module, ep.Endpoint.Path, ep.Endpoint.Exposes}
	}
	if want := (exposure{"ledger", "/books/entries/{id}", "ledger.entries/command/record"}); got != want {
		t.Errorf("the module's exposure: %+v, want %+v", got, want)
	}
	if e := g.Edge("ledger.entries/endpoint/record|dispatches|ledger.entries/command/record"); e == nil || !e.Declared {
		t.Errorf("the exposure's edge: %+v", e)
	}
}

// The product runs a module's command over HTTP under the prefix, and in
// process, alike.
func TestAModulesOperationsRunUnderItsPrefix(t *testing.T) {
	app := start(t, kit.Mount(LedgerModule, kit.Prefix("/books/")))
	if r := call(t, app, "POST /books/entries/e1", LedgerEntry{Amount: 5}); r.status != http.StatusNoContent {
		t.Fatalf("recording over HTTP, under the prefix: %d %s", r.status, r.body)
	}
	if _, err := LedgerRecord.Dispatch(t.Context(), LedgerEntry{ID: "e2", Amount: 7}); err != nil {
		t.Fatalf("recording in process: %v", err)
	}
	if total, err := LedgerTotal.Ask(t.Context(), kit.EmptyValue{}); err != nil || total != 12 {
		t.Errorf("the ledger's total: %d, %v", total, err)
	}
}
