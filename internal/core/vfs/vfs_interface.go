// Package vfs — the write contract and its ADR 0039 capability sibling.
//
// They live apart from vfs.go because that file carries the package's doc and
// the one thing that is NOT an interface declaration: the FS alias that says
// reading is io/fs unchanged.
package vfs

import "io/fs"

// WritableFS is the write half, and it is FROZEN at four methods on top of the
// embedded [FS]. A new capability arrives as a sibling interface reached by
// type assertion — never by widening this one (ADR 0039): pkg/v1/vfs aliases
// it, Go interfaces are structural, and a fifth method would break every
// downstream implementation at compile time with no deprecation window.
//
// Every method validates its name through [ValidateWritePath], and every
// method taking a mode validates it through [ValidatePerm], so the two
// implementations refuse identical inputs identically. That is what makes an
// in-memory filesystem usable as a test double for a real one.
//
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the concrete filesystems stay unexported behind their
// constructors in internal/service/vfs.
type WritableFS interface {
	FS
	// WriteFile writes data to name, creating the file if it is absent and
	// truncating it if it is not, with perm applied on creation. Missing
	// parent directories are NOT created: a typo in a path is a far more
	// common event than a genuinely absent directory, and inventing the
	// tree silently is how a file ends up somewhere nobody looks. It is not
	// atomic — [AtomicWriter] is the call that is.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	// MkdirAll creates name and every missing parent, applying perm to each
	// directory it creates. An existing directory is success; an existing
	// non-directory is [NotRegularFile].
	MkdirAll(name string, perm fs.FileMode) error
	// Remove removes one file or one EMPTY directory. A non-empty directory
	// is [DirectoryNotEmpty] rather than a recursive delete, because the two
	// are different decisions and only one of them is reversible by accident.
	Remove(name string) error
	// RemoveAll removes name and everything beneath it. A name that does not
	// exist is success — the caller asked for it to be gone, and it is.
	RemoveAll(name string) error
}

// AtomicWriter publishes a whole file in one indivisible step. It is the ADR
// 0039 sibling of [WritableFS]: reached by type assertion, never by widening
// the port, because it is a CAPABILITY and not every filesystem has one — an
// object store with no rename, a read-through overlay, a tar archive.
//
//	if publisher, ok := filesystem.(vfs.AtomicWriter); ok {
//		err = publisher.WriteAtomic("index.html", page, 0o644)
//	}
//
// Both filesystems in internal/service/vfs implement it.
type AtomicWriter interface {
	// WriteAtomic publishes data at name so that a concurrent reader sees
	// either the previous content or the new content, and never a partial
	// file, a truncated one, or a name that resolves to nothing.
	//
	// The contract on FAILURE is why this method exists, and it is what
	// [PublishFailed] asserts: when WriteAtomic returns an error, the bytes
	// previously at name are unchanged, and no temporary file survives
	// anywhere the caller can see. An implementation that cannot promise
	// both must not implement this interface.
	//
	// Durability beyond the process is the implementation's to describe: the
	// operating-system filesystem flushes the file AND the directory entry,
	// while an in-memory one has no device to flush and says so.
	WriteAtomic(name string, data []byte, perm fs.FileMode) error
}

// FullFS is the union of [WritableFS] and [AtomicWriter], and it is what the
// SDK's own constructors return.
//
// It is the shape core/metrics already uses for FullMeter, and it exists for
// the same reason: the port stays frozen and the capability stays assertable
// for a THIRD-PARTY filesystem that cannot publish atomically, while a caller
// wiring one of the SDK's own does not have to type-assert for the headline
// feature. A parameter should still ask for the narrowest thing it uses —
// WritableFS when it only writes, AtomicWriter when it only publishes.
type FullFS interface {
	WritableFS
	AtomicWriter
}
