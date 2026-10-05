package errs

// registrySentinel is a compile-time marker that the registry audit is
// expected to run in this package. It has no runtime behaviour beyond
// existing.
const registrySentinel string = "sdk-registry-audit-v1"

// RegistryMarker returns the registry sentinel so external callers (and
// the audit test itself) can confirm the package is the one expected to
// own the SDK-wide code allocation audit.
func RegistryMarker() string {
	//: return the constant unchanged — purely documentary.
	return registrySentinel
}
