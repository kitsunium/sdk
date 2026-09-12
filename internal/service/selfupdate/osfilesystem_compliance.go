// Package selfupdate — the compile-time proof that osFileSystem still satisfies
// the core FileSystem port.
// Package updater — compile-time assertion for osFileSystem.
//
// Hoisted from osfilesystem.go per KTN-IFACE-ASSERT-PLACEMENT so the
// production source carries no purely verificational declarations.
package selfupdate

// : Asserts at compile time that *osFileSystem satisfies FileSystem.
var _ FileSystem = (*osFileSystem)(nil)
