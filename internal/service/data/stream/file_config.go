package stream

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// streamDirMode is the mode a stream directory, and each directory below
// it, is created with when absent: owner-only.
const streamDirMode fs.FileMode = 0o700

// fileMode is the mode of the lock file and of every cursor: owner-only.
const fileMode fs.FileMode = 0o600

// cursorsDir is the directory under the stream directory that holds the
// consumer groups' cursors.
const cursorsDir = "cursors"

// lockName is the file under the stream directory whose lock the open engine
// holds.
const lockName = "lock"

// FileConfig configures [NewFile]: the directory that holds the stream, the
// clock that stamps and ages its records, and the policy it enforces.
type FileConfig struct {
	// Clock stamps [corestream.EntryValue].AppendedAt, ages records for
	// [corestream.PolicyValue].RetainFor, and times the wait
	// [corestream.PolicyValue].BlockTimeout allows. The instants are kept
	// in the segments, so a stream reopened on another clock ages its
	// records by that one. Nil means clock.System.
	Clock clock.Timed
	// Dir is the stream directory: one segmented log per partition under
	// p<partition>/, the groups' cursors under cursors/. It is created
	// owner-only when absent, refused when it exists and an account
	// outside the owner and group could replace its entries, and its
	// subdirectories are refused besides when one is a symbolic link, not a
	// directory, or writable by such an account at all — the queue's rule
	// (on Windows, read from the access-control list). One engine at a time
	// opens a directory: a second, in this process or another, is refused
	// while the first is open.
	Dir string
	// Policy is what the stream enforces; its zero value works. Its
	// Partitions cannot shrink across a reopen: a directory holding a
	// partition the policy no longer counts is refused.
	Policy corestream.PolicyValue
}

// prepareRoot creates the stream directory, or checks it when it already
// exists: the queue's root rule, a sticky world-writable directory
// accepted.
func prepareRoot(dir string) error {
	//: an empty Dir would resolve to the working directory, never what was meant.
	if dir == "" {
		//: StreamDirectoryUnusable, naming the field.
		return errs.Wrap(corestream.StreamDirectoryUnusable, errs.WrapParams{}, errs.String("field", "Dir"))
	}
	//: a stream nested under a fresh state directory is the ordinary shape.
	if mkErr := os.MkdirAll(dir, streamDirMode); mkErr != nil {
		//: a file where the directory belongs is the caller's fault, not the medium's.
		if info, statErr := os.Stat(dir); statErr == nil && !info.IsDir() {
			//: StreamDirectoryUnusable, by shape.
			return directoryUnusable(dir, "not-a-directory", "")
		}
		//: the medium refused.
		return backendFailed("mkdir", dir, mkErr)
	}
	info, statErr := os.Stat(dir)
	//: a directory that cannot be described cannot be checked.
	if statErr != nil {
		//: the medium could not answer.
		return backendFailed("stat", dir, statErr)
	}
	why, observed, unusable := rootUnusableBecause(dir, info)
	//: a directory no other account can replace an entry of.
	if !unusable {
		//: usable.
		return nil
	}
	//: StreamDirectoryUnusable, naming the value and why.
	return directoryUnusable(dir, why, observed)
}

// prepareSub creates the directory name under dir owner-only when absent,
// then checks what is there, whoever put it there: the queue's state rule,
// one notch stricter than the root's, since its entries are the records or
// the cursors themselves.
//
// It is os.Mkdir and os.Lstat, never MkdirAll or Stat, which follow a
// symbolic link: a subdirectory that is one is refused whatever it points
// at, or the records would live wherever the link's author chose.
func prepareSub(dir, name string) (string, error) {
	sub := filepath.Join(dir, name)
	//: an existing entry is left for the check to judge.
	if mkErr := os.Mkdir(sub, streamDirMode); mkErr != nil && !os.IsExist(mkErr) {
		//: the medium refused.
		return "", backendFailed("mkdir", sub, mkErr)
	}
	info, statErr := os.Lstat(sub)
	//: a directory that cannot be described cannot be checked.
	if statErr != nil {
		//: the medium could not answer.
		return "", backendFailed("lstat", sub, statErr)
	}
	why, observed, unusable := subUnusableBecause(sub, info)
	//: a real directory no other account can write an entry into.
	if !unusable {
		//: usable.
		return sub, nil
	}
	//: StreamDirectoryUnusable, naming the subdirectory.
	return "", directoryUnusable(sub, why, observed)
}

// rootUnusableBecause is the root rule: refused by shape, then when an
// account outside the owner and group can REPLACE an entry.
func rootUnusableBecause(dir string, info fs.FileInfo) (why, observed string, unusable bool) {
	//: a link, or anything that is not a directory, before any permission.
	if why, unusable = indirectionOrFile(info); unusable {
		//: refused by shape.
		return why, "", true
	}
	//: the platform's rule (dirtrust_*.go).
	return rootWritableByAnyone(dir, info)
}

// subUnusableBecause is the subdirectories' rule: refused by shape, then
// when an account outside the owner and group can put an entry there at all.
func subUnusableBecause(dir string, info fs.FileInfo) (why, observed string, unusable bool) {
	//: a link, or anything that is not a directory, before any permission.
	if why, unusable = indirectionOrFile(info); unusable {
		//: refused by shape.
		return why, "", true
	}
	//: the platform's rule (dirtrust_*.go).
	return subWritableByAnyone(dir, info)
}

// indirectionOrFile refuses the two shapes no permission makes usable: a
// symbolic link or another reparse point, and an entry that is not a
// directory.
func indirectionOrFile(info fs.FileInfo) (why string, unusable bool) {
	//: a link keeps the records wherever its author chose.
	if info.Mode()&fs.ModeSymlink != 0 || reparsePoint(info) {
		//: refused before any permission is read.
		return "symlink", true
	}
	//: a file where a directory belongs.
	if !info.IsDir() {
		//: a configuration fault.
		return "not-a-directory", true
	}
	//: a real directory; its permissions decide.
	return "", false
}

// directoryUnusable refuses the directory value for why, with what the
// verdict was read from when there is one.
func directoryUnusable(value, why, observed string) error {
	fields := []errs.FieldValue{errs.String("field", "Dir"), errs.String("value", value), errs.String("why", why)}
	//: a verdict by permission says what it read.
	if observed != "" {
		fields = append(fields, errs.String("observed", observed))
	}
	//: StreamDirectoryUnusable.
	return errs.Wrap(corestream.StreamDirectoryUnusable, errs.WrapParams{}, fields...)
}

// backendFailed reports an operation on path the storage refused, cause
// kept in the chain: under STREAM_BACKEND_FAILED for a standard library
// cause, in the trail of an SDK one, whose code wins (rule 6).
func backendFailed(op, path string, cause error) error {
	//: the operation and the path, never a record's bytes.
	return errs.Wrap(cause, corestream.StreamBackendFailed.Params(),
		errs.String("op", op), errs.String("path", path))
}
