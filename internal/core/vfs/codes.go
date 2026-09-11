// Package vfs — range 0.2.25.* (ADR 0056 core/vfs block).
package vfs

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.25.0 - 0.2.25.255

// CodeInvalidPath identifies a name that is not a valid filesystem path for
// this domain: rooted, empty, backslash-separated, or carrying a "." or ".."
// element. It is the LEXICAL half of the confinement guarantee.
const CodeInvalidPath errs.Code = 0x00_02_19_01 // 0.2.25.1

// CodeInvalidPermission identifies a file mode that cannot be honoured as
// written: zero — which the SDK refuses to read as "pick a default" — or bits
// outside fs.ModePerm, which would make a typo able to mint a setuid file.
const CodeInvalidPermission errs.Code = 0x00_02_19_02 // 0.2.25.2

// CodePathEscaped identifies a lexically valid name that resolved OUTSIDE the
// filesystem's root — a symbolic link pointing away from the tree, or a
// traversal the operating system refused. It is the half fs.ValidPath cannot
// see.
const CodePathEscaped errs.Code = 0x00_02_19_03 // 0.2.25.3

// CodeReadFailed identifies an open, stat or directory listing the filesystem
// refused. The cause is wrapped rather than replaced, so errors.Is against
// fs.ErrNotExist and fs.ErrPermission keeps answering.
const CodeReadFailed errs.Code = 0x00_02_19_04 // 0.2.25.4

// CodeWriteFailed identifies a create, write, mkdir or remove the filesystem
// refused. As with CodeReadFailed the cause stays in the chain.
const CodeWriteFailed errs.Code = 0x00_02_19_05 // 0.2.25.5

// CodePublishFailed identifies an atomic publication that did not complete.
// Its contract is the interesting part: the previous content is intact and no
// temporary file survives.
const CodePublishFailed errs.Code = 0x00_02_19_06 // 0.2.25.6

// CodeNotRegularFile identifies a name that exists and is not a regular file,
// where writing it would replace a directory, a device or a socket.
const CodeNotRegularFile errs.Code = 0x00_02_19_07 // 0.2.25.7

// CodeDirectoryNotEmpty identifies a Remove aimed at a directory that still
// has entries. RemoveAll is the call that was wanted.
const CodeDirectoryNotEmpty errs.Code = 0x00_02_19_08 // 0.2.25.8
