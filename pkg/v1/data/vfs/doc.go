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
// [AtomicWriter].WriteAtomic writes into a temporary in the SAME directory,
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
