// Package session — the file store's construction parameters.
package session

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// FileConfig configures the on-disk store. The two timeouts and the clock mean
// exactly what they mean in [Config] — the fields are repeated rather than
// embedded so a call site reads as one flat literal — and Dir and Key are the
// two things only a persistent store needs.
//
// Key is REQUIRED and has no default. Every record is sealed with it before it
// touches the disk, so a store built without one would write session contents
// in plain text next to a filename an operator can list. There is no value the
// SDK could invent here: a key it generated would be lost on restart, taking
// every session with it, and a key derived from the directory name would not be
// a key at all.
type FileConfig struct {
	// IdleTimeout is how long a session may go untouched. Refused when
	// non-positive, and when it is not strictly shorter than AbsoluteTimeout.
	IdleTimeout time.Duration
	// AbsoluteTimeout is the ceiling from the identifier's minting instant.
	AbsoluteTimeout time.Duration
	// Clock is the time source; nil falls back to clock.System.
	Clock clock.Clock
	// Dir is the directory records live in. It is created with mode 0700 if
	// absent, and REFUSED if it exists with any group or world bit set — a
	// session directory another user can read is the whole compromise.
	Dir string
	// Key is the 256-bit AEAD key every record is sealed under. The seal binds
	// the record to its own filename, so an attacker with write access to the
	// directory cannot make one session's ciphertext answer for another's
	// identifier.
	Key corecrypto.Key
}

// window is the validated deadline policy this configuration describes.
func (c FileConfig) window() window {
	//: a nil clock is a working configuration, so it is filled, not refused.
	clk := c.Clock
	//: fall back to the wall clock.
	if clk == nil {
		//: the shared, concurrency-safe system reading.
		clk = clock.System
	}
	//: validation has already run by the time this is called.
	return window{idle: c.IdleTimeout, absolute: c.AbsoluteTimeout, clk: clk}
}

// validate refuses a configuration the file store cannot honour.
func (c FileConfig) validate() error {
	//: the timeout rules are shared with Config so the two cannot drift.
	if err := validateWindow(c.IdleTimeout, c.AbsoluteTimeout); err != nil {
		//: InvalidConfig, naming the field.
		return err
	}
	//: a store with nowhere to write is not a store.
	if c.Dir == "" {
		//: never default to the working directory or to os.TempDir: both are
		//: readable by other users on a shared host, and both would make the
		//: refusal this branch exists for silently disappear.
		return wrapAs(coresession.InvalidConfig, nil, kerrs.String("field", "Dir"))
	}
	//: a zero Key yields nil bytes; a wrong-length one is refused the same way.
	if len(c.Key.Bytes()) != corecrypto.KeyLen {
		//: the key material itself is never named in the error.
		return wrapAs(coresession.InvalidConfig, nil, kerrs.String("field", "Key"))
	}
	//: a usable configuration.
	return nil
}
