//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/vfs .

// Package vfs is the public facade for the SDK's filesystem domain: reading
// stays io/fs, writing gets a contract, and publishing a file becomes one call
// that either replaces it completely or changes nothing at all.
//
//	site, err := vfs.NewOS("/srv/www")
//	if err != nil {
//		return err
//	}
//
//	// generated beside, swapped by rename(2), both flushed — one call
//	if err := site.WriteAtomic("index.html", page, 0o644); err != nil {
//		return err // the previous index.html is still being served
//	}
//
// # Reading is io/fs, and this package does not reimplement it
//
// [FS] is a type ALIAS of io/fs.FS, and [WritableFS] embeds it. So a
// filesystem from this package IS an fs.FS, and fs.WalkDir, fs.Glob,
// fs.ReadFile, fs.Sub, fs.Stat and every library that already accepts an
// fs.FS work on it with no adapter:
//
//	pages, err := fs.Glob(site, "posts/*.md")
//	err = fs.WalkDir(site, "assets", func(p string, d fs.DirEntry, err error) error { … })
//
// There is deliberately no vfs.Walk and no vfs.Glob. The stdlib ones are
// correct and already reachable; a second pair would only be a second place
// for a bug to live.
//
// # WriteAtomic, and what it promises when it fails
//
// [AtomicWriter.WriteAtomic] writes into a temporary in the SAME directory,
// flushes it to the device, renames it over the target, and then flushes the
// directory entry. Each step is there for a reason the previous one does not
// cover: the same directory means the rename cannot cross a device, the file
// flush means the rename is not publishing page-cache, and the directory flush
// means the name survives a crash alongside the bytes it points at.
//
// The interesting half is the failure. If WriteAtomic returns [PublishFailed],
// the bytes previously at that name are unchanged — byte for byte — and no
// temporary was left behind. That is not a description of the code, it is an
// assertion with a test behind it: the SDK sabotages each step in turn and
// compares a hash of the destination before and after.
//
// One error means the opposite and has its own name. [DirectorySyncFailed]
// arrives only AFTER a successful rename: the new content is what every reader
// now sees, and the only thing in doubt is whether the directory entry
// survives a power loss. It is deliberately not rolled back.
//
// # Paths, and what confinement does and does not cover
//
// Every name is a slash-separated, unrooted, "."- and ".."-free path — exactly
// fs.ValidPath, the rule io/fs already imposes on readers. "../../etc/passwd"
// is [InvalidPath]; it is refused, never normalised, because every normaliser
// is a small parser and every small parser has a case its author missed.
//
// On top of that, [NewOS] resolves every name against a held directory handle,
// so a name that would leave the tree is refused by the KERNEL — including one
// that only leaves it by traversing a symbolic link. And a WRITE whose final
// component is a symbolic link is refused outright with [PathEscaped], even
// when the link points somewhere perfectly legal: the caller named one file,
// and the bytes would have landed on another.
//
// # Permissions are refused, never defaulted
//
// A zero mode returns [InvalidPermission]. 0644 and 0600 differ by who may
// read the bytes, and the SDK does not know what the bytes are, so it declines
// to choose — and a zero mode is what an unfilled struct field looks like.
// setuid, setgid, sticky and the type bits are refused too, so a mistyped
// octal literal cannot mint a setuid file.
//
// # Two implementations
//
// [NewOS] is the real one. [NewMem] is a filesystem held in a map, for testing
// the code that uses one:
//
//	filesystem := vfs.NewMem() // no temporary directory, no cleanup
//
// They answer the same typed refusals for the same inputs — that is what makes
// the second one a double rather than a different filesystem with matching
// method names — and a table-driven conformance suite runs the same cases
// against both. Three differences are real and are stated rather than
// discovered: the memory filesystem has no symbolic links, reports the zero
// ModTime, and makes no durability claim, because it has no device to make one
// about.
//
// # Where NewOS refuses to run
//
// The disk filesystem needs a rename that atomically replaces, a directory
// handle that can be flushed, and permission bits that mean something. Where
// any of those is missing — Windows today — [NewOS] returns
// proc.UnsupportedPlatform at CONSTRUCTION rather than shipping a filesystem
// that would report success for a durability claim it cannot keep. [NewMem]
// works everywhere.
package vfs

import (
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// FS is the public alias for the read half, which is io/fs.FS unchanged. It
// exists so a signature can say "an SDK filesystem" without implying that
// anything about reading is different here.
type FS = corevfs.FS

// WritableFS is the public alias for the write half: the four write verbs on
// top of the embedded [FS]. It is frozen — a new capability arrives as a
// sibling interface, never as a fifth method.
type WritableFS = corevfs.WritableFS

// AtomicWriter is the public alias for the publication capability, reached by
// type assertion on a filesystem this package did not build.
type AtomicWriter = corevfs.AtomicWriter

// FullFS is the public alias for the union both constructors return: a
// [WritableFS] that is also an [AtomicWriter]. A parameter should still ask
// for the narrowest thing it uses.
type FullFS = corevfs.FullFS

var (
	// InvalidPath is returned for a name outside the fs.ValidPath grammar —
	// rooted, empty, or carrying a "." or ".." element — and for the root
	// itself on any call that writes or removes.
	InvalidPath = corevfs.InvalidPath
	// InvalidPermission is returned for a zero file mode, which is never read
	// as "pick a default", and for any mode carrying setuid, setgid, sticky
	// or a type bit.
	InvalidPermission = corevfs.InvalidPermission
	// PathEscaped is returned when a name resolved outside the filesystem's
	// root, and when a write's final component is a symbolic link.
	PathEscaped = corevfs.PathEscaped
	// ReadFailed is returned when an open, stat or listing was refused. The
	// cause stays in the chain, so errors.Is(err, fs.ErrNotExist) still
	// answers.
	ReadFailed = corevfs.ReadFailed
	// WriteFailed is returned when a create, write, mkdir or remove was
	// refused, and keeps its cause the same way.
	WriteFailed = corevfs.WriteFailed
	// PublishFailed is returned when an atomic publication did not complete.
	// The previous content is intact and no temporary survives, so the
	// operation is safe to retry and safe to abandon.
	PublishFailed = corevfs.PublishFailed
	// NotRegularFile is returned when the name — or a component of it —
	// exists and is not the kind of thing the call needs it to be.
	NotRegularFile = corevfs.NotRegularFile
	// DirectoryNotEmpty is returned by Remove for a directory that still has
	// entries. RemoveAll is the recursive call.
	DirectoryNotEmpty = corevfs.DirectoryNotEmpty
	// RootUnavailable is returned by NewOS when the root cannot be opened:
	// absent, not a directory, or not searchable by this process.
	RootUnavailable = svcvfs.RootUnavailable
	// DirectorySyncFailed is returned when the rename succeeded and the
	// directory entry could not be flushed. The content IS published; only
	// its survival across a power loss is in doubt, and it is deliberately
	// not rolled back.
	DirectorySyncFailed = svcvfs.DirectorySyncFailed
)

// NewOS opens root as a filesystem confined to that directory tree.
//
// It returns proc.UnsupportedPlatform on a GOOS that lacks the mechanics this
// filesystem's guarantees rest on, and [RootUnavailable] when the directory
// cannot be opened. Both are construction-time refusals, so a misconfiguration
// is reported where the program is wired rather than on the first write.
//
// The filesystem holds the directory's descriptor and additionally implements
// io.Closer, which releases it; every call after Close fails.
func NewOS(root string) (filesystem FullFS, err error) {
	//: delegate to the service constructor.
	return svcvfs.NewOS(root)
}

// NewMem returns an empty in-memory filesystem containing only its root.
//
// It takes no arguments on purpose: every knob it could offer would be one a
// consumer's test has to set before it can assert anything, and the value of
// this type is that a filesystem double costs one line.
func NewMem() FullFS {
	//: delegate to the service constructor.
	return svcvfs.NewMem()
}
