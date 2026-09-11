// Package vfs — the disk-backed filesystem: its constructor, its read half,
// and the guards every write goes through.
package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// osFS is the operating-system filesystem, confined to one directory tree.
type osFS struct {
	// root holds the directory handle every name is resolved against. It is
	// what makes confinement a kernel property rather than a string check.
	root *os.Root
	// read is root.FS(), which already implements fs.StatFS, fs.ReadDirFS and
	// fs.ReadFileFS — so the read half delegates instead of reimplementing.
	read fs.FS
	// ops carries the publication mechanics. It is a field rather than a set
	// of direct calls so publish() stays a pure function of them, which is
	// what lets the internal test provoke a failure at each step.
	ops atomicOps
}

// NewOS opens root as a confined filesystem.
//
// The refusal order is deliberate. The PLATFORM is checked before the path:
// on a GOOS without the mechanics this filesystem needs, no directory would
// make it work, and reporting "no such directory" for a name that exists would
// send the reader looking in the wrong place entirely.
func NewOS(root string) (filesystem corevfs.FullFS, err error) {
	//: ADR 0018 — the typed refusal, at wiring time, where it is actionable.
	if !platformNative {
		//: proc.UnsupportedPlatform, the same sentinel every unimplemented
		//: primitive in the SDK returns.
		return nil, coreproc.UnsupportedPlatform
	}
	handle, openErr := os.OpenRoot(root)
	//: a root that cannot be opened will fail every call it is ever given.
	if openErr != nil {
		//: RootUnavailable — the cause stays in the chain, so a caller may
		//: still ask errors.Is(err, fs.ErrNotExist).
		return nil, failRoot(openErr, kerrs.String("root", root))
	}
	disk := &osFS{root: handle, read: handle.FS()}
	//: bind the native mechanics once; nothing swaps them in production.
	disk.ops = disk.nativeOps()
	//: a filesystem that publishes atomically, confined to root.
	return disk, nil
}

// Close releases the directory descriptor the filesystem was opened on.
//
// It satisfies io.Closer, a capability reached by type assertion (ADR 0039)
// rather than a fifth verb on the frozen WritableFS — the session file store's
// shape. os.Root also closes itself once it is garbage collected, but a caller
// opening filesystems in a loop should get its descriptors back when it says
// so, not at the next collection. Every call after Close fails.
func (o *osFS) Close() error {
	//: the one handle this filesystem owns.
	if closeErr := o.root.Close(); closeErr != nil {
		//: RootUnavailable, with the operating system's cause in the chain.
		return failRoot(closeErr, kerrs.String("op", "close"))
	}
	//: released.
	return nil
}

// failRoot wraps a constructor cause onto [RootUnavailable] by restating it,
// so the operating-system error survives in the chain.
func failRoot(cause error, fields ...kerrs.FieldValue) error {
	//: same restatement shape as failRead / failWrite / failPublish.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:     CodeRootUnavailable,
		Reason:   "ROOT_UNAVAILABLE",
		Public:   "The filesystem root could not be opened",
		Private:  "service/vfs: os.OpenRoot failed — the path is absent, is not a directory, or is not searchable by this process",
		ExitCode: exitConfig,
	}, fields...)
}

// Open resolves name and returns it as an fs.File.
func (o *osFS) Open(name string) (file fs.File, err error) {
	//: the lexical grammar first, so a malformed name gets the typed verdict
	//: rather than the stdlib's "invalid argument".
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	opened, openErr := o.read.Open(name)
	//: ReadFailed, with the *fs.PathError intact underneath.
	if openErr != nil {
		//: no half-open handle escapes alongside an error.
		return nil, failRead(openErr, kerrs.String("path", name))
	}
	//: the caller owns the handle.
	return opened, nil
}

// Stat implements fs.StatFS, so fs.Stat and fs.WalkDir take the fast path.
//
// It is not a member of [corevfs.WritableFS] and must not become one — it is
// discovered by the standard library's own type assertion, which is the same
// sibling mechanism ADR 0039 prescribes, arriving here from the other side.
func (o *osFS) Stat(name string) (info fs.FileInfo, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	stat, statErr := o.root.Stat(name)
	//: ReadFailed.
	if statErr != nil {
		//: nothing partial escapes.
		return nil, failRead(statErr, kerrs.String("path", name))
	}
	//: the resolved metadata.
	return stat, nil
}

// ReadDir implements fs.ReadDirFS. Entries are sorted by filename, which io/fs
// requires and the delegate already guarantees.
func (o *osFS) ReadDir(name string) (entries []fs.DirEntry, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	listed, readErr := fs.ReadDir(o.read, name)
	//: ReadFailed.
	if readErr != nil {
		//: a partial listing is never returned with an error.
		return nil, failRead(readErr, kerrs.String("path", name))
	}
	//: sorted, as io/fs promises.
	return listed, nil
}

// ReadFile implements fs.ReadFileFS.
func (o *osFS) ReadFile(name string) (content []byte, err error) {
	//: same grammar as Open.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return nil, pathErr
	}
	content, readErr := o.root.ReadFile(name)
	//: ReadFailed.
	if readErr != nil {
		//: no partial content alongside an error.
		return nil, failRead(readErr, kerrs.String("path", name))
	}
	//: the whole file.
	return content, nil
}

// guardFileTarget is the check every call that writes a FILE runs first.
//
// It refuses a symbolic link before it refuses anything else, and that order
// carries the security argument: os.Root would follow the link and would keep
// the result inside the root, so nothing escapes — but the bytes would land on
// whatever the link names rather than on the name the caller gave. That is the
// symlink plant, and it is worth its own verdict.
//
// The check is an Lstat, so it judges the state it OBSERVED. A link planted
// between this call and the open that follows is still followed; the property
// that holds without a race is confinement to the root, and that one belongs
// to the kernel.
func (o *osFS) guardFileTarget(name string, perm fs.FileMode) error {
	//: path grammar, then mode, then the state on disk.
	if guardErr := guardNameAndPerm(name, perm); guardErr != nil {
		//: InvalidPath or InvalidPermission.
		return guardErr
	}
	info, statErr := o.root.Lstat(name)
	//: an absent name is the ordinary case: there is nothing to follow.
	if errors.Is(statErr, fs.ErrNotExist) {
		//: clear to create.
		return nil
	}
	//: an Lstat that failed for any other reason — a parent that is a file, a
	//: path the kernel refused to resolve — is classified rather than
	//: stepped over.
	if statErr != nil {
		//: NotRegularFile naming the blocker, or WriteFailed.
		return o.classifyBlocked(name, statErr)
	}
	//: something is there; decide what it is.
	return refuseKind(name, info.Mode(), info.Mode().IsRegular())
}

// guardDirTarget is guardFileTarget's counterpart for MkdirAll: an existing
// DIRECTORY is the success case there, not a collision.
func (o *osFS) guardDirTarget(name string, perm fs.FileMode) error {
	//: same two lexical guards.
	if guardErr := guardNameAndPerm(name, perm); guardErr != nil {
		//: InvalidPath or InvalidPermission.
		return guardErr
	}
	info, statErr := o.root.Lstat(name)
	//: nothing there is exactly what MkdirAll is for.
	if errors.Is(statErr, fs.ErrNotExist) {
		//: clear to create.
		return nil
	}
	//: any other Lstat failure is classified the same way.
	if statErr != nil {
		//: NotRegularFile naming the blocker, or WriteFailed.
		return o.classifyBlocked(name, statErr)
	}
	//: an existing directory is idempotent success.
	return refuseKind(name, info.Mode(), info.Mode().IsDir())
}

// blockedAncestor reports the first component of name's PARENT chain that
// exists and is not a directory.
//
// The operating system reports ENOTDIR from the middle of a resolution and
// does not say which component caused it. An in-memory filesystem knows
// immediately. Walking here is what makes the two answer the same thing, and
// the answer names the component rather than the request — which is the
// difference between "that write failed" and "delete the file called blocker".
func (o *osFS) blockedAncestor(name string) (blocker string, blocked bool) {
	parts := strings.Split(path.Dir(name), "/")
	//: Stat, not Lstat: an intermediate symlink pointing at a directory is a
	//: perfectly good parent, and os.Root has already confined it.
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		//: a top-level target has no ancestor chain to walk.
		if prefix == "." {
			//: nothing above it.
			continue
		}
		info, statErr := o.root.Stat(prefix)
		//: a component that does not resolve is not the blocker.
		if statErr != nil {
			//: keep walking; a later one may still be the answer.
			continue
		}
		//: the first non-directory in the chain is the reportable cause.
		if !info.IsDir() {
			//: found it.
			return prefix, true
		}
	}
	//: the chain is clear; whatever failed, this is not why.
	return "", false
}

// classifyBlocked turns a failure into the most useful verdict available:
// NotRegularFile naming the component that is in the way, when there is one,
// and the filesystem's own account otherwise.
func (o *osFS) classifyBlocked(name string, cause error) error {
	//: a file where a directory has to be is the caller's answer.
	if blocker, blocked := o.blockedAncestor(name); blocked {
		//: NotRegularFile, naming the blocker rather than the request.
		return verdict(corevfs.NotRegularFile, kerrs.String("path", blocker))
	}
	//: nothing in the chain explains it; keep the cause.
	return failWrite(cause, kerrs.String("path", name))
}

// guardNameAndPerm applies the two core guards both write paths share.
func guardNameAndPerm(name string, perm fs.FileMode) error {
	//: the write grammar refuses the root as well as every escape.
	if pathErr := corevfs.ValidateWritePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	//: ADR 0031 — a zero mode is refused, never defaulted.
	return corevfs.ValidatePerm(perm)
}

// refuseKind turns an observed file mode into the verdict for it. acceptable
// is what the calling verb considers a legal existing object.
func refuseKind(name string, mode fs.FileMode, acceptable bool) error {
	//: a symbolic link gets the security verdict, not the generic one.
	if mode&fs.ModeSymlink != 0 {
		//: PathEscaped — a write here would land on the link's target.
		return verdict(corevfs.PathEscaped, kerrs.String("path", name))
	}
	//: the verb's own idea of a legal occupant.
	if acceptable {
		//: proceed.
		return nil
	}
	//: NotRegularFile — a directory, device or socket is in the way.
	return verdict(corevfs.NotRegularFile, kerrs.String("path", name))
}
