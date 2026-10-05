package selfupdate

// : Asserts at compile time that *stdIOCopier satisfies Copier.
var _ Copier = (*stdIOCopier)(nil)
