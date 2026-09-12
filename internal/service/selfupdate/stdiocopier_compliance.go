// Package selfupdate — the compile-time proof that stdIOCopier still satisfies
// the core Copier port.
// Package updater — compile-time assertion for stdIOCopier.
//
// Hoisted from stdiocopier.go per KTN-IFACE-ASSERT-PLACEMENT so the
// production source carries no purely verificational declarations.
package selfupdate

// : Asserts at compile time that *stdIOCopier satisfies Copier.
var _ Copier = (*stdIOCopier)(nil)
