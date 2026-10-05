package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// revisions is Revisions's body: decl_gen.go writes Revisions, from the
// design, as one call of it.
func revisions(n int) StoreOption {
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
