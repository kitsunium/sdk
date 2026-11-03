// Package s3 is a core/data/vfs FullFS over one S3 bucket, or one prefix of it
// (ADR 0056's port, ADR 0211). It lives in the third-party/aws module beside
// the s3 logger writer, on the vendor the module already carries (ADR 0157).
//
// # The mapping
//
// A file is an object whose key is Config.Prefix joined with its name; a
// directory is a zero-byte marker object whose key ends in a slash, written
// by MkdirAll, or any prefix that some key extends. Open, Stat and ReadFile
// are HeadObject and GetObject; ReadDir is ListObjectsV2 with the slash
// delimiter, paged by Config.MaxKeys; WriteFile and WriteAtomic are one
// PutObject; Remove is DeleteObject, refused on a directory that still holds
// something; RemoveAll is a paged DeleteObjects.
//
// # Atomic publication without a rename
//
// A PutObject is atomic per key: a reader sees the complete old object or the
// complete new one, never a part, and a failed PutObject leaves the old one in
// place. That is exactly AtomicWriter's contract, so WriteAtomic is the same
// request as WriteFile; the port has no Rename, and none is faked. The two
// differ in their mode only: WriteFile keeps the mode of the file it
// rewrites, WriteAtomic records perm on every publication.
//
// # What S3 does not have
//
// Modes are recorded as object metadata (ModeMetadata) and reported by Stat;
// S3 enforces none of them. Durability is S3's: a PutObject that returned is
// stored. ModTime is the object's Last-Modified, to the second. A missing
// parent is refused as on disk, so a directory exists only once MkdirAll, or
// a write beneath it by another client, made it. Every call is bounded by
// Config.Timeout, since the port's methods take no context.
package s3
