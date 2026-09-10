// Package vfs — declares the sentinel *errs.Error outcomes this layer owns.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The port's own verdicts live in internal/core/vfs; only the two outcomes a
// CONCRETE filesystem can produce and an abstract one cannot are declared here.
package vfs

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// RootUnavailable is returned by NewOS when the root cannot be opened.
	//
	// It is a CONSTRUCTION-time refusal on purpose: a filesystem whose root
	// does not exist will fail every call it is ever given, and the useful
	// place to learn that is where the program is assembled, not on the first
	// request that happens to write something.
	RootUnavailable = errs.Define(CodeRootUnavailable, "ROOT_UNAVAILABLE",
		"The filesystem root could not be opened",
		"service/vfs: os.OpenRoot failed — the path is absent, is not a directory, or is not searchable by this process",
		errs.WithExitCode(exitConfig))

	// DirectorySyncFailed is returned when the rename succeeded and the
	// parent directory could not be flushed afterwards.
	//
	// It deliberately does NOT roll back. The rename has already happened, so
	// the new content is what every reader sees; undoing it would mean a
	// second non-atomic write to repair a durability problem, which is how a
	// good file gets replaced by a worse one. The caller is told exactly what
	// is true — published, not proven durable — and decides whether that is
	// acceptable for its data.
	DirectorySyncFailed = errs.Define(CodeDirectorySyncFailed, "DIRECTORY_SYNC_FAILED",
		"The file was published but the directory entry was not flushed",
		"service/vfs: fsync on the parent directory failed AFTER a successful rename; the content is visible and may not survive a power loss — deliberately not rolled back",
		errs.WithExitCode(exitIOErr))
)
