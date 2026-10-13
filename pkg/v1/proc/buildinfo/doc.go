// Package buildinfo is what the running binary was built from: the toolchain,
// the main package, the main module with its version-control stamp, and every
// dependency followed through its replacement.
//
//	build, ok := buildinfo.Build()
//	sdk, found := build.Module("github.com/kitsunium/sdk")
//	// sdk.Version is a release ("v0.4.6"), or "" with sdk.Revision/sdk.Time
//	// for a pseudo-version, or "" with sdk.Local for a directory.
//
// A module's recorded version conflates three things, and [Module] keeps them
// apart: a RELEASE in Version, a COMMIT in Revision and Time (a pseudo-version
// names one, and so does the main module's version-control stamp), and a
// DIRECTORY in Local and Dir (a replace, a workspace module). Nothing here fails:
// a binary built without module support reads as false.
//
// The same values are re-exported by pkg/v1/proc/process, beside the spawning of
// children and the process's runtime statistics. This package is the reading
// alone, so that a program asking only what it was built from imports neither
// the spawn engine nor runtime/metrics, whose initialisation every process
// importing them pays at start (ADR 0172).
package buildinfo
