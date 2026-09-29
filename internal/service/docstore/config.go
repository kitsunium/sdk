// Package docstore — the store's construction parameters and the index
// declarations, and the refusals a configuration no store could honour gets.
package docstore

import (
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultFoldAt is the least number of overlay entries that folds the overlay
// into the snapshot, when [Config].FoldAt is zero.
//
// A fold rewrites the whole snapshot, so how often it runs decides what a
// write costs on average. Folding once the overlay holds as many entries as
// the store holds documents keeps that average a constant — one fold of N
// documents per N writes — and the floor keeps a small store from rewriting
// its snapshot every few writes for nothing: a thousand small files in one
// directory is not a cost worth avoiding.
const DefaultFoldAt int = 1024

// overlaySuffix names the overlay directory beside the snapshot.
const overlaySuffix string = ".d"

// versionsSuffix names the versions file beside the snapshot: every
// document's former versions, as the last fold left them (ADR 0143).
const versionsSuffix string = ".versions"

// Config configures [Open]. Key is required; every other field is optional,
// and leaving all of them empty opens a working store kept in memory. The
// secondary indexes are Open's other arguments.
type Config[T any] struct {
	// Key returns the key a document is stored under. It must be a pure
	// function of the document: the store calls it to file every write and
	// compares its answers to refuse an update that would rename a document.
	Key func(T) string
	// FS is where the store persists, and Path is the snapshot's name inside
	// it — an io/fs name such as "shop/items.json". The overlay lives beside
	// the snapshot, in the directory Path + ".d". A nil FS keeps the store in
	// memory: nothing survives the process, and nothing is written anywhere.
	FS corevfs.FullFS
	// Clock stamps each version with the instant of the write that made it.
	// Nil is the system clock.
	Clock clock.Clock
	// Held reports whether the document stored under key is held — a legal
	// hold — so that no write prunes its versions: they pile up beyond
	// Versions until a write finds the document released, and that write
	// prunes them. It is called with the document's key by a write that would
	// prune, under the writers' lock, so it may read this store and must not
	// write to it; a panic in it reaches the writer, and nothing is written.
	// Nil holds nothing. It is refused without Versions.
	Held func(key string) bool
	// Path is the snapshot's name inside FS. It is required with FS and
	// refused without it, because a path with nowhere to be written is a
	// store that would silently keep nothing.
	Path string
	// FoldAt is how many overlay entries fold the overlay into the snapshot
	// on the write that reaches it. Zero is the default rule: as many entries
	// as the store holds documents, and at least [DefaultFoldAt]. A negative
	// value never folds on a write; Fold, Open and Close still do.
	FoldAt int
	// Versions is how many former versions each document keeps beside its
	// current one, the newest: a write that changes a document makes a new
	// version, and prunes the oldest beyond Versions in the same durable
	// write. Zero keeps no versions — the store writes exactly the files it
	// wrote before versions existed — and a negative value is refused. A
	// persistent store keeps them in the file Path + ".versions" once a fold
	// ran, and a store opened with zero over files that keep versions is
	// refused rather than left to drop or corrupt them (ADR 0143).
	Versions int
}

// IndexSpec declares one secondary index: a name to read it by, whether it is
// unique, and the function that gives a document its keys in it. Build one
// with [Unique] or [Index], and give it to [Open]. The indexes are rebuilt
// when the store opens and kept in step with every write.
type IndexSpec[T any] struct {
	// Keys returns a document's keys in the index. Empty keys are not
	// indexed, and a key listed twice is filed once.
	Keys func(T) []string
	// Name reads the index in Lookup and Find, and names it in a refusal.
	Name string
	// Unique refuses a write that would file two documents under one key.
	Unique bool
}

// Unique declares a unique index over the one key key returns: no two
// documents may share it, and a write that would is refused with
// UniqueKeyTaken. An empty key is not indexed, so any number of documents may
// have none.
func Unique[T any](name string, key func(T) string) IndexSpec[T] {
	//: a nil function stays nil, for Open to refuse by name.
	if key == nil {
		//: the declaration, without a way to compute its keys.
		return IndexSpec[T]{Name: name, Unique: true}
	}
	//: one key, or none when it is empty.
	return IndexSpec[T]{Name: name, Unique: true, Keys: func(v T) []string {
		//: the empty key is not a key.
		if k := key(v); k != "" {
			//: the one key.
			return []string{k}
		}
		//: nothing to file.
		return nil
	}}
}

// Index declares an index where a document may have several keys and a key
// several documents — the members a task is shared with. Find reads it.
func Index[T any](name string, keys func(T) []string) IndexSpec[T] {
	//: the declaration as given; Open checks it.
	return IndexSpec[T]{Name: name, Keys: keys}
}

// validate refuses a configuration no store could honour, with the indexes
// Open was given, before anything touches a filesystem.
func (c *Config[T]) validate(indexes []IndexSpec[T]) error {
	//: a store that cannot key a document cannot store one.
	if c.Key == nil {
		//: StoreMisconfigured, naming the setting.
		return misconfigured("Key", "nil")
	}
	//: where the store persists, when it does.
	if pathErr := c.validatePath(); pathErr != nil {
		//: StoreMisconfigured, naming the setting.
		return pathErr
	}
	//: how many versions, and a hold only where versions are kept.
	if versionsErr := validateVersions(c.Versions, c.Held != nil); versionsErr != nil {
		//: StoreMisconfigured, naming the setting.
		return versionsErr
	}
	//: the declarations every engine refuses alike.
	return validateIndexes(indexes)
}

// validateVersions refuses a negative number of versions, and a hold on a
// store that keeps none: a caller who sets Held believes versions are kept.
// Both engines refuse through it.
func validateVersions(versions int, held bool) error {
	//: no number of versions is negative.
	if versions < 0 {
		//: StoreMisconfigured, naming the setting.
		return misconfigured("Versions", "negative")
	}
	//: a hold keeps versions from pruning, and there would be none.
	if held && versions == 0 {
		//: StoreMisconfigured, naming the setting.
		return misconfigured("Held", "set without Versions")
	}
	//: a number of versions the store can keep.
	return nil
}

// validateIndexes refuses index declarations no engine could serve: an index
// with no name, two of one name, or one with no key function. Both Open and
// OpenSQL call it, so a declaration refused by one engine is refused by the
// other.
func validateIndexes[T any](indexes []IndexSpec[T]) error {
	//: every index has a distinct name and a function.
	seen := make(map[string]bool, len(indexes))
	//: in declaration order, so the first mistake is the one reported.
	for _, spec := range indexes {
		//: an index nobody can name cannot be read.
		if spec.Name == "" {
			//: StoreMisconfigured, naming the problem.
			return misconfigured("Indexes", "an index has no name")
		}
		//: two indexes of one name make every read of it ambiguous.
		if seen[spec.Name] {
			//: StoreMisconfigured, naming the index.
			return kerrs.Wrap(StoreMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "Indexes"), kerrs.String("problem", "declared twice"), kerrs.String("index", spec.Name))
		}
		seen[spec.Name] = true
		//: an index that cannot compute a key files nothing.
		if spec.Keys == nil {
			//: StoreMisconfigured, naming the index.
			return kerrs.Wrap(StoreMisconfigured, kerrs.WrapParams{},
				kerrs.String("setting", "Indexes"), kerrs.String("problem", "no key function"), kerrs.String("index", spec.Name))
		}
	}
	//: declarations an engine can serve.
	return nil
}

// validatePath checks the persistence settings: both or neither, and a path
// that names a file — and an overlay directory — the filesystem can write.
func (c *Config[T]) validatePath() error {
	//: in memory: a path would be a promise nothing keeps.
	if c.FS == nil {
		//: a path without a filesystem is refused, not ignored.
		if c.Path != "" {
			//: StoreMisconfigured, naming the setting.
			return misconfigured("Path", "set without FS")
		}
		//: a memory store.
		return nil
	}
	//: the snapshot's name and the overlay's are both write targets.
	if pathErr := corevfs.ValidateWritePath(c.Path); pathErr != nil {
		//: StoreMisconfigured, with the vfs verdict's message as a field.
		return kerrs.Wrap(StoreMisconfigured, kerrs.WrapParams{},
			kerrs.String("setting", "Path"), kerrs.String("problem", "not a writable io/fs name"), kerrs.String("cause", pathErr.Error()))
	}
	//: a persistent store.
	return nil
}

// misconfigured is StoreMisconfigured naming the setting and the problem.
func misconfigured(setting, problem string) error {
	//: the two fields every configuration refusal carries.
	return kerrs.Wrap(StoreMisconfigured, kerrs.WrapParams{},
		kerrs.String("setting", setting), kerrs.String("problem", problem))
}
