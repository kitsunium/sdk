// Package route — range 0.3.18.* (ADR 0005 service/logger/middleware/route block).
package route

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.18.0 - 0.3.18.255

// CodeRouteNoMatch identifies a Write call whose record matched none of
// the registered predicates. The default sink (when configured) handles
// this case; without a default the Write returns this sentinel.
const CodeRouteNoMatch errs.Code = 0x00_03_12_01 // 0.3.18.1

// NoMatch is returned by Write when no predicate matched the record AND
// no default sink was configured. Callers that want a catch-all should
// pass a default sink to New so writes never silently disappear.
var NoMatch = errs.Define(CodeRouteNoMatch, "ROUTE_NO_MATCH",
	"No route predicate matched the record",
	"service/logger/middleware/route.Write found no matching predicate and no default sink")
