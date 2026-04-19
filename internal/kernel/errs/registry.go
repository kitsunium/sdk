// Package errs: registry.go is a placeholder companion to the SDK-wide
// AST audit (registry_external_test.go). The audit only needs a backing
// source file to satisfy the ktn-linter KTN-TEST-FILES convention; there
// is no runtime registry today — code allocation lives in ADR 0002 and
// the embedded table of the audit test.
package errs

// registrySentinel is a compile-time marker that the registry audit is
// expected to run in this package. It has no runtime behaviour beyond
// existing.
const registrySentinel string = "sdk-registry-audit-v1"

// RegistryMarker returns the registry sentinel so external callers (and
// the audit test itself) can confirm the package is the one expected to
// own the SDK-wide code allocation audit.
//
// Returns:
//   - string: the registrySentinel constant.
func RegistryMarker() (marker string) {
	//: return the constant unchanged — purely documentary.
	return registrySentinel
}
