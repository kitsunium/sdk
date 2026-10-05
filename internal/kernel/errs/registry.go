package errs

// registrySentinel is a compile-time marker that the registry audit is
// expected to run in this package. It has no runtime behaviour beyond
// existing.
const registrySentinel string = "sdk-registry-audit-v1"

// registryMarker is RegistryMarker's body: decl_gen.go writes RegistryMarker, from the
// design, as one call of it.
func registryMarker() string {
	//: return the constant unchanged — purely documentary.
	return registrySentinel
}
