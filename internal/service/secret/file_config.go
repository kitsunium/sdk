// Package secret — the file store's construction parameters and the checks
// that refuse a directory the store cannot keep secrets in.
package secret

import (
	"errors"
	"io/fs"
	"os"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// storeDirMode is the mode the store's directory is CREATED with. An existing
// directory is checked, never changed: silently narrowing a mode an operator
// chose would hide the finding from them.
const storeDirMode fs.FileMode = 0o700

// recordMode is the mode every record is published with.
const recordMode fs.FileMode = 0o600

// otherAccountBits are the group and world permission bits. A store directory
// carrying any of them lets another account list — or worse — the secrets.
const otherAccountBits fs.FileMode = 0o077

// FileConfig parameterises [NewFile].
//
// Its zero value is deliberately NOT a working store: Dir must be stated,
// because the directory IS the store, and a default one — the working
// directory, the temporary directory — would put secrets wherever the process
// happened to start, readable by whoever else uses that place.
type FileConfig struct {
	// Dir is the directory the store owns: one record per secret, published
	// atomically, 0600, plus the lock files that serialise writers. It is
	// created 0700 if absent, and REFUSED if it exists with any group or world
	// permission bit — a secret directory another account can read is the
	// whole compromise, and the store does not chmod it behind the operator's
	// back.
	Dir string
	// Key, when set, SEALS every record with AES-256-GCM before it touches
	// the disk, bound to the secret's name so a record copied under another
	// name does not open. Nothing readable is written then — not the values,
	// not the version numbers, not the timestamps; only the file names, which
	// are the secret names.
	//
	// The zero Key means no encryption at rest, which is a legitimate choice
	// on a volume that is already encrypted, and it is the choice a caller
	// makes by leaving the field empty. A store opened with a Key refuses a
	// record written without one, and the other way round, with
	// RecordUnreadable — never by guessing which it was.
	Key corecrypto.Key
	// Clock stamps each version's Created and paces the wait for another
	// process's lock. nil means clock.System.
	Clock clock.Timed
}

// validate refuses a configuration no store could honour, before anything
// touches the disk.
func (c FileConfig) validate() error {
	//: a store with nowhere to write is not a store.
	if c.Dir == "" {
		//: InvalidConfig, naming the setting.
		return wrapAs(InvalidConfig, nil, errs.String("setting", "Dir"), errs.String("problem", "empty"))
	}
	//: a key is either absent or a real 256-bit key; nothing in between.
	if keyLen := len(c.Key.Bytes()); keyLen != 0 && keyLen != corecrypto.KeyLen {
		//: InvalidConfig, naming the setting and never the material.
		return wrapAs(InvalidConfig, nil, errs.String("setting", "Key"), errs.String("problem", "not 32 bytes"))
	}
	//: usable as given.
	return nil
}

// clockOrSystem resolves the configured time source.
func (c FileConfig) clockOrSystem() clock.Timed {
	//: nil is the production default, not a misconfiguration.
	if c.Clock == nil {
		//: the system time source, reading and waiting.
		return clock.System
	}
	//: the injected source.
	return c.Clock
}

// prepareDir creates dir 0700 when it is absent and checks it when it is not.
// The check is the point: an existing directory another account can read is
// refused, never narrowed.
func prepareDir(dir string) error {
	info, statErr := os.Stat(dir)
	//: absent — create it owner-only, then check it like any other, so the
	//: umask cannot widen what was asked for without the check noticing.
	if errors.Is(statErr, fs.ErrNotExist) {
		//: parents are created too; each gets the same owner-only mode.
		if mkErr := os.MkdirAll(dir, storeDirMode); mkErr != nil {
			//: InvalidConfig: the directory the caller named cannot exist.
			return wrapAs(InvalidConfig, nil, errs.String("setting", "Dir"), errs.String("problem", "cannot be created"))
		}
		info, statErr = os.Stat(dir)
	}
	//: anything else that stops a stat is a directory this store cannot use.
	if statErr != nil {
		//: InvalidConfig, without the path.
		return wrapAs(InvalidConfig, nil, errs.String("setting", "Dir"), errs.String("problem", "cannot be inspected"))
	}
	//: a file where the directory should be.
	if !info.IsDir() {
		//: InvalidConfig.
		return wrapAs(InvalidConfig, nil, errs.String("setting", "Dir"), errs.String("problem", "not a directory"))
	}
	//: group or world bits: another account can at least list the secrets.
	if info.Mode().Perm()&otherAccountBits != 0 {
		//: InvalidConfig, naming the fix.
		return wrapAs(InvalidConfig, nil, errs.String("setting", "Dir"),
			errs.String("problem", "accessible to other accounts; chmod 700 it"))
	}
	//: an owner-only directory.
	return nil
}
