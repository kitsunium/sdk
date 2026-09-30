// Package kit — revisions: a record keeps its versions, diffed and
// restored.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Revisions keeps the n previous versions of each record of the store, n
// from 1 to 100, as WordPress keeps a post's revisions: every write that
// changes a record makes a version, numbered from 1 and never renumbered,
// stamped with when it was made, the caller's user and the command that
// made it; the oldest beyond n are pruned by the write that makes a newer
// one, unless a legal hold keeps the record.
//
//	var Pages = Service.Store("pages", Page.Key, kit.Revisions(20))
//
//	revs, err := Pages.Revisions(ctx, id)                            // newest first
//	edits, err := Pages.Diff(ctx, id, revs[3].Number, revs[0].Number)
//	page, err := Pages.Restore(ctx, id, revs[3].Number)              // a new version
//	page, err = Pages.Restore(ctx, id, revs[3].Number, "/title")     // the title only
//
// A store with Revisions(n) holds up to n+1 times its records: in memory
// with the store in the data directory, in the table <table>___vs on a
// database, which kit's migrations create.
func Revisions(n int) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Revisions(n)
}

// Revision is one version of a record a store with [Revisions] keeps: its
// number — from 1, never given twice while the record exists —, when the
// write that made it ran, on the app's clock, zero for a record stored before
// its store kept revisions; who made it — the caller's [UserID], empty for a
// write with no user —; which command made it — its node ID, empty outside a
// command —; and the record as it was, its secret members zeroed.
type Revision[T any] = ikit.RevisionEvent[T]

// Edit is one change between two versions of a record, in RFC 6902's words:
// Op is add, remove or replace, Path the RFC 6901 JSON pointer of the member
// it changes, From the value it had and To the value it has, as JSON. A
// secret member says it changed, never what: its From and To are empty.
type Edit = ikit.Edit
