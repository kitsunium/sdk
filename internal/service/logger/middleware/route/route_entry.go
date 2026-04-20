// Package route: route_entry.go declares the RouteParams struct that pairs
// a predicate with a downstream sink. Pulled out of router_sink.go so the
// router file stays focused on the Sink contract.
package route

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// Predicate decides whether a route should accept a given record. The
// router evaluates predicates in declaration order; the first true wins.
type Predicate func(r corelogger.RecordEvent) (match bool)

// RouteParams binds a Predicate to a downstream Sink. Held verbatim by the
// router; callers MUST NOT mutate the Sink reference after registration.
type RouteParams struct {
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
//   - Predicate: a closure usable as RouteParams.When.
func LevelAtLeast(min level.Level) (p Predicate) {
	//: closure binds min so the route table stays declarative at the call site.
	return func(r corelogger.RecordEvent) (match bool) {
		//: standard >= comparison; level.Level supports it natively.
		return r.Level >= min
	}
}
