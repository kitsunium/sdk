// Package lock — the file locker's configuration and the two ADR 0031 halves
// it contains: one clamp and one refusal, in one struct, for contrast.
package lock

import (
	"io/fs"
	"os"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
)

// defaultPoll is the interval between flock(2) attempts while another PROCESS
// holds the lock.
//
// It is a clamp, not a refusal, and the difference from MemoryConfig.TTL is
// the point of ADR 0031: a poll interval has one obviously-right order of
// magnitude — short enough that a handover is not noticeable, long enough that
// a contended lock does not spin a core — and no caller's correctness depends
// on the exact value. A lease lifetime has neither property.
const defaultPoll time.Duration = 25 * time.Millisecond

// lockDirMode is the mode a lock directory is CREATED with when it does not
// exist. An existing directory is checked, never changed — see [checkDir].
const lockDirMode fs.FileMode = 0o700

// lockFileMode is the mode a lock file is created with.
const lockFileMode fs.FileMode = 0o600

// FileConfig parameterises [NewFileLocker].
//
// Its zero value is deliberately NOT a working locker: Dir must be stated,
// because the directory IS the lock's scope. Poll, by contrast, has a
// defensible default and is clamped — the two ADR 0031 halves, side by side.
type FileConfig struct {
	// Dir holds one lock file per name. It is created 0700 if absent, and
	// CHECKED if it already exists: a world-writable directory without the
	// sticky bit is refused, because anyone able to unlink a lock file can
	// replace its inode and split one lock into two.
	//
	// It MUST be set. There is no default: a temporary directory would make
	// the lock's SCOPE — which processes it excludes — depend on a value
	// nobody chose.
	Dir string
	// Poll is the interval between attempts while another process holds the
	// lock. Zero means [defaultPoll]; negative is refused.
	Poll time.Duration
	// Clock is the time source the poll waits on, injectable so contention is
	// testable without sleeping. nil means clock.System.
	Clock clock.Timed
}

// validate applies ADR 0031: clamp the poll interval, refuse everything the
// SDK cannot choose on the caller's behalf.
func (c FileConfig) validate() error {
	//: the scope of a file lock IS its directory. Inventing one would make
	//: "which processes does this exclude" a question the caller never
	//: answered, and two services with different defaults would silently fail
	//: to exclude each other.
	if c.Dir == "" {
		//: refuse, naming the field.
		return kerrs.Wrap(corelock.LockMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "Dir"),
			kerrs.String("value", ""))
	}
	//: a negative interval is a mistake, not a third meaning: read as a
	//: non-positive timer duration it would fire instantly and turn the wait
	//: into a spin on a contended lock.
	if c.Poll < 0 {
		//: refuse rather than reinterpret.
		return kerrs.Wrap(corelock.LockMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "Poll"),
			kerrs.String("value", c.Poll.String()))
	}
	//: usable as given.
	return nil
}

// pollOrDefault resolves the configured poll interval.
func (c FileConfig) pollOrDefault() time.Duration {
	//: zero is "no preference", which here has a defensible answer.
	if c.Poll == 0 {
		//: the clamp half of ADR 0031.
		return defaultPoll
	}
	//: the caller's value, validated positive.
	return c.Poll
}

// clockOrSystem resolves the configured time source.
func (c FileConfig) clockOrSystem() clock.Timed {
	//: a nil clock is the production default, not a misconfiguration.
	if c.Clock == nil {
		//: the system time source.
		return clock.System
	}
	//: the injected source, as given.
	return c.Clock
}

// prepareDir audits the path, then creates dir if it is absent and checks it
// if it is not.
//
// The chain audit runs FIRST, before anything is created. os.MkdirAll follows
// an indirection at a parent component, so auditing afterwards would mean
// refusing the directory only after having created it inside whatever tree the
// indirection pointed at.
func prepareDir(dir string) error {
	//: the components ABOVE the lock file, which O_NOFOLLOW cannot reach —
	//: see chain.go.
	if redirected := checkChain(dir); redirected != nil {
		//: LockPathRedirected or LockBackendFailed.
		return redirected
	}
	info, err := os.Stat(dir)
	//: absent — create it owner-only, then check it like any other. On Unix a
	//: directory we created has the mode we asked for and the check is a
	//: formality; on WINDOWS it inherits its parent's ACL and lockDirMode
	//: means nothing, so a fresh lock directory under an Everyone-writable
	//: parent is exactly as unsafe as an existing one and used to be accepted
	//: because this branch returned here.
	if os.IsNotExist(err) {
		//: MkdirAll rather than Mkdir: a lock directory nested under a fresh
		//: state directory is the ordinary deployment shape.
		if mkErr := os.MkdirAll(dir, lockDirMode); mkErr != nil {
			//: the medium refused.
			return backendFailed("mkdir", dir, mkErr)
		}
		return checkCreated(dir)
	}
	//: some other stat failure.
	if err != nil {
		//: the medium could not answer.
		return backendFailed("stat", dir, err)
	}
	//: a regular file where a directory belongs is a configuration fault.
	if !info.IsDir() {
		//: refuse, naming the field.
		return kerrs.Wrap(corelock.LockMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "Dir"),
			kerrs.String("value", dir))
	}
	//: the safety check, whose rule and whose justification are BOTH
	//: platform-specific — see dirsafety_posix.go and dirsafety_windows.go.
	return checkDir(dir, info)
}

// checkCreated applies the safety rule to a directory this process has just
// created.
//
// It is not a formality on every platform. A directory created on Windows
// INHERITS its parent's access control list, and lockDirMode has no meaning
// there at all, so "we made it, therefore it is safe" holds on Unix and is
// false on Windows.
func checkCreated(dir string) error {
	info, err := os.Stat(dir)
	//: the directory was created and cannot be described, which is a medium
	//: fault rather than a verdict.
	if err != nil {
		//: LockBackendFailed.
		return backendFailed("stat", dir, err)
	}
	//: the same rule an existing directory gets.
	return checkDir(dir, info)
}
