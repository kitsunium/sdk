// Package vfs — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public string here names a path. A Public is read by third parties, and a
// path is the one piece of caller data a filesystem error is guaranteed to
// hold; it travels as a log-only field instead, reachable through errs.FieldsOf.
package vfs

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65). The caller handed the domain
// something it cannot act on; the program is fine, the argument is not.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77). A confinement refusal is a
// security verdict, not an I/O accident, and deserves to be distinguishable
// from one in a supervisor's exit status.
const exitNoPerm int = 77

// exitConfig matches sysexits EX_CONFIG (78). A refused mode is a permanent
// wiring fault: the same call will be refused identically forever, and the fix
// is an edit at the call site.
const exitConfig int = 78

// exitIOErr matches sysexits EX_IOERR (74). The filesystem itself failed.
const exitIOErr int = 74

var (
	// InvalidPath is returned for a name outside the fs.ValidPath grammar,
	// and by [ValidateWritePath] for the root itself.
	//
	// It is a REFUSAL rather than a cleanup. Normalising "../../etc/passwd"
	// into something safe is the single most reliable way to ship a
	// traversal bug: every normaliser is a small parser, every small parser
	// has a case its author did not think of, and the caller never learns
	// that the path it asked for is not the path it got.
	InvalidPath = errs.Define(CodeInvalidPath, "INVALID_PATH",
		"That path is not usable on this filesystem",
		"core/vfs: name violates fs.ValidPath (rooted, empty, backslash-separated, or carrying a . or .. element) or targets the root; refused, never normalised",
		errs.WithExitCode(exitDataErr))

	// InvalidPermission is returned for a zero file mode, or one carrying
	// bits outside fs.ModePerm.
	//
	// ADR 0031's refusing half: any mode the SDK invented here would be
	// arbitrary. 0644 and 0600 differ by who may read the bytes, and the
	// caller is the only party that knows what the bytes are — so a zero is
	// read as "the field was never filled in", which it almost always is.
	InvalidPermission = errs.Define(CodeInvalidPermission, "INVALID_PERMISSION",
		"A file mode is required and must be a plain permission mode",
		"core/vfs: perm is zero (refused, never defaulted — ADR 0031) or carries bits outside fs.ModePerm such as setuid, setgid or sticky",
		errs.WithExitCode(exitConfig))

	// PathEscaped is returned when a lexically valid name resolved outside
	// the filesystem's root.
	//
	// fs.ValidPath cannot see this: every element of "link/secret" is legal,
	// and whether it leaves the tree depends on what "link" points at, which
	// is a runtime property of the filesystem and not of the string.
	PathEscaped = errs.Define(CodePathEscaped, "PATH_ESCAPED",
		"That path leaves the filesystem it was resolved against",
		"core/vfs: the name resolved outside the root — a symbolic link pointing away from the tree, or a traversal the kernel refused",
		errs.WithExitCode(exitNoPerm))

	// ReadFailed is returned when an open, stat or directory listing was
	// refused. The cause stays in the error chain, so a caller may still ask
	// errors.Is(err, fs.ErrNotExist) and get the answer io/fs would give.
	ReadFailed = errs.Define(CodeReadFailed, "READ_FAILED",
		"The filesystem could not read that path",
		"core/vfs: open, stat or readdir failed; the *fs.PathError cause is wrapped, not replaced, so errors.Is against fs.ErrNotExist still answers",
		errs.WithExitCode(exitIOErr))

	// WriteFailed is returned when a create, write, mkdir or remove was
	// refused. Like [ReadFailed] it keeps the cause reachable.
	WriteFailed = errs.Define(CodeWriteFailed, "WRITE_FAILED",
		"The filesystem could not write that path",
		"core/vfs: create, write, mkdir or remove failed; the *fs.PathError cause is wrapped, not replaced",
		errs.WithExitCode(exitIOErr))

	// PublishFailed is returned when an atomic publication did not complete.
	//
	// Its guarantee is the whole point of the domain and is asserted by a
	// test, not by this comment: on this error the bytes previously at the
	// name are unchanged and no temporary file survives. It is therefore
	// safe to retry, and safe to give up.
	PublishFailed = errs.Define(CodePublishFailed, "PUBLISH_FAILED",
		"The filesystem could not publish that file",
		"core/vfs: atomic publication aborted; the previous content is intact and the temporary was removed — the operation is safe to retry",
		errs.WithExitCode(exitIOErr))

	// NotRegularFile is returned when a name exists and is not a regular
	// file. Writing it would replace a directory, a device or a socket, and
	// no caller has ever meant that by WriteFile.
	NotRegularFile = errs.Define(CodeNotRegularFile, "NOT_REGULAR_FILE",
		"That path already holds something other than a file",
		"core/vfs: the name resolves to a directory, device, socket or symlink; a write there would destroy an object of a different kind",
		errs.WithExitCode(exitDataErr))

	// DirectoryNotEmpty is returned by Remove for a directory that still has
	// entries — POSIX ENOTEMPTY, made typed and made identical in both
	// implementations so a memory filesystem is a faithful double.
	DirectoryNotEmpty = errs.Define(CodeDirectoryNotEmpty, "DIRECTORY_NOT_EMPTY",
		"That directory still has entries in it",
		"core/vfs: Remove targets one file or one EMPTY directory; RemoveAll is the recursive call",
		errs.WithExitCode(exitDataErr))
)
