// Package route — declares the sentinels returned by the route
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package route

import "github.com/kitsunium/sdk/internal/kernel/errs"

// NoMatch is returned by Write when no predicate matched the record AND
// no default sink was configured. Callers that want a catch-all should
// pass a default sink to New so writes never silently disappear.
var NoMatch = errs.Define(CodeRouteNoMatch, "ROUTE_NO_MATCH",
	"No route predicate matched the record",
	"service/logger/middleware/route.Write found no matching predicate and no default sink")
