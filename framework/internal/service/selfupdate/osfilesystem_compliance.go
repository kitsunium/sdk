package selfupdate

var (
	// : Asserts at compile time that *osFileSystem satisfies FileSystem.
	_ FileSystem = (*osFileSystem)(nil)

	// : Asserts at compile time that osFileSystem keeps the Link sibling
	// : probe.go reaches by type assertion (ADR 0150).
	_ linker = osFileSystem{}
)
