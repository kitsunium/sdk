// Package selfupdate — the compile-time proof that osFileSystem still satisfies
// the core FileSystem port.
// Package updater — compile-time assertion for osFileSystem.
//
// Hoisted from osfilesystem.go per KTN-IFACE-ASSERT-PLACEMENT so the
// production source carries no purely verificational declarations.
package selfupdate

var (
	// : Asserts at compile time that *osFileSystem satisfies FileSystem.
	_ FileSystem = (*osFileSystem)(nil)

	// : Asserts at compile time that osFileSystem keeps the Link sibling
	// : probe.go reaches by type assertion (ADR 0146).
	_ linker = osFileSystem{}
)
