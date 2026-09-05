// Package harness — a domain's group of conformance checks.
package harness

// Check exercises one public-API behaviour and returns its Result.
//
// A Check must not panic; it converts any failure into a Fail or Skip Result
// with detail. The runner recovers anyway — one misbehaving check must not take
// the conformance run down — but a recovered panic is recorded as a failure of
// the check rather than of the behaviour it was exercising, which is a far less
// useful thing to read in the table.
type Check func() Result

// CheckGroup is a domain's named group of checks, returned by each
// checks/<domain>.go.
//
// The domain is carried alongside the checks rather than repeated inside each
// Result, so a check cannot label itself as belonging to a domain it was not
// registered under — which is what keeps the per-domain table honest when a
// check is moved between files.
type CheckGroup struct {
	// Domain labels every Result the group produces.
	Domain string
	// Checks are run in order; each is independent.
	Checks []Check
}
