// Package route — declares the sentinel the routing Sink returns. Its Reason
// is the namespaced form of its code's name (ADR 0006).
package route

import "github.com/kitsunium/sdk/internal/kernel/errs"

// NoMatch is returned by Write when no predicate matched the record AND
// no default sink was configured. Callers that want a catch-all should
// pass a default sink to New so writes never silently disappear.
var NoMatch = errs.Define(CodeRouteNoMatch, "ROUTE_NO_MATCH",
	"No route predicate matched the record",
	"service/observe/logger/middleware/route.Write found no matching predicate and no default sink")
