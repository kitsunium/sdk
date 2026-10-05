// Package events — hosts the engine: registration, removal, and the
// copy-on-write membership the dispatch reads.
//
// Package events — hosts the TYPED registration front end: the generic
// functions that hide the erasure the heterogeneous bus is built on.
//
// Package events — hosts the dispatch: the ordered walk, the halt, the panic
// guard, and the aggregate.
//
// Package events — one published version of the bus membership, and the two
// rewrites that produce the next one.
package events
