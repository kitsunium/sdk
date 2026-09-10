// Package vfs declares the SDK's filesystem port. Reading is io/fs — this
// package does not redeclare it, does not wrap it and does not reimplement its
// walkers. What it adds is the half the standard library deliberately left
// out: writing, directory creation and removal, and publication that is
// ATOMIC. A core sibling admitted by ADR 0056.
//
// # What comes for free, and must never be written again
//
// [WritableFS] EMBEDS fs.FS. That one line is the whole interoperability
// story: every implementation of this port IS an fs.FS, so fs.WalkDir,
// fs.Glob, fs.ReadFile, fs.Sub, fs.Stat and every third-party consumer of
// fs.FS work on it unchanged and with no adapter. There is deliberately no
// vfs.Walk and no vfs.Glob in this SDK. Adding one would not be a feature: the
// stdlib versions are correct, maintained by the Go project, and already
// reachable through the embedded interface.
//
// The read verbs are therefore absent from this file on purpose. If a method
// you want already exists on fs.FS or as an io/fs package function, this port
// does not restate it.
//
// # Why the domain exists at all
//
// Because "generate beside, swap by rename, flush the parent directory" is an
// invariant the SDK repeats by hand in every publisher, and a discipline
// repeated by hand is a discipline that will be forgotten exactly once. Here
// it is a PRIMITIVE, with its failure path tested rather than assumed:
// [AtomicWriter] promises that a caller which observes an error observes the
// previous bytes, byte for byte, and finds no temporary file left behind.
//
// # The path grammar, and what it does and does not guarantee
//
// Every name handed to this port is a slash-separated, unrooted, "."- and
// ".."-free path — exactly fs.ValidPath, which is already what fs.FS requires
// of readers. [ValidatePath] is that check; [ValidateWritePath] adds the one
// extra refusal a writer needs, namely that the root itself is not a target.
//
// That is a LEXICAL guarantee and it is not the whole answer: a path made only
// of legal elements can still leave the tree by traversing a symbolic link
// that points outside it. Refusing that is the implementation's job, because
// only the implementation knows what a link is — and it reports [PathEscaped]
// when it happens. An implementation that cannot enforce it MUST refuse to be
// constructed rather than accept a name it cannot confine.
//
// # Permissions are never inferred
//
// A zero mode is refused, never read as "use a default" (ADR 0031). Any file
// mode the SDK could pick would be arbitrary: 0644 and 0600 differ by who may
// read the bytes, and only the caller knows what the bytes are. Bits outside
// fs.ModePerm — setuid, setgid, sticky, the type bits — are refused BY NAME
// for the same reason a typo must not be able to mint a setuid file.
//
// The concrete filesystems live in internal/service/vfs; this package owns the
// contract, the two guards, and the typed sentinels.
package vfs

import "io/fs"

// FS is the read half of a filesystem, and it is io/fs.FS unchanged.
//
// It is an ALIAS rather than a fresh interface, so a vfs.FS and an fs.FS are
// the same type to the compiler and no conversion, adapter or assertion sits
// between an SDK filesystem and the standard library's walkers. The name
// exists to say, in one place, that the SDK's answer for reading is the
// stdlib's answer for reading.
type FS = fs.FS
