// Package route: codes.go — range 3800-3899 reserved for the route Sink.
// Codes are declared at source as typed constants; the errs registry audit
// verifies uniqueness and range membership.
package route

// range: 3800-3899

// CodeRouteNoMatch identifies a Write call whose record matched none of
// the registered predicates. The default sink (when configured) handles
// this case; without a default the Write returns this sentinel.
const CodeRouteNoMatch int = 3801
