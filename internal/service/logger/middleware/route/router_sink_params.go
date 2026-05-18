// Package route: router_sink_params.go declares the Params struct that
// pairs a predicate with a downstream sink. Held as a parent-prefixed
// sibling of router_sink.go so the router file stays focused on the Sink
// contract while preserving the KTN-STRUCT-COLOCATE convention.
package route

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// Predicate decides whether a route should accept a given record. The
// router evaluates predicates in declaration order; the first true wins.
type Predicate func(r corelogger.RecordEvent) (match bool)

// Params binds a Predicate to a downstream Sink. Held verbatim by the
// router; callers MUST NOT mutate the Sink reference after registration.
// Read at call sites as route.Params{When: …, Sink: …}.
type Params struct {
	// When decides whether this entry accepts the record.
	When Predicate
	// Sink receives the matching record.
	Sink corelogger.Sink
}

// LevelAtLeast returns a Predicate matching records at or above min.
//
// Params:
//   - min: minimum severity level the entry accepts.
//
// Returns:
//   - Predicate: a closure usable as Params.When.
func LevelAtLeast(min level.Level) Predicate {
	//: closure binds min so the route table stays declarative at the call site.
	return func(r corelogger.RecordEvent) (match bool) {
		//: standard >= comparison; level.Level supports it natively.
		return r.Level >= min
	}
}
