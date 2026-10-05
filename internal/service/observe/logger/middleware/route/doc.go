// Package route implements a predicate-based router Sink. Records are
// dispatched to the first downstream sink whose Predicate returns true
// for the record. When no predicate matches, the optional fallback sink
// receives the write; without a fallback, Write returns NoMatch.
//
// Use case: send error-level records to a remote alerting drain while
// keeping info-level records local.
//
// Package route — declares the Params struct that pairs a predicate with a
// downstream sink. Sibling of router_sink.go so the router file stays
// focused on the Sink contract.
package route
