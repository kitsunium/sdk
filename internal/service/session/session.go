// Package session implements the server-side session domain declared in
// internal/core/session (ADR 0045): two concrete stores — one in memory, one on
// disk — and the AEAD sealer that renders a session identifier as a cookie
// value.
//
// Both stores answer the same contract and differ only in where the record
// lives and how long it survives. The memory store dies with the process; the
// file store survives a restart, is confined to one host, and makes real
// operating-system guarantees that this package refuses to fake where the
// mechanism does not exist (see file_store.go and fsguard_unix.go).
//
// Nothing here waits on the wall clock. Every deadline is read from an injected
// kernel/clock.Clock, so an expiry test advances a ManualClock instead of
// sleeping. The narrow half of the port is deliberate: a session store READS
// time, it never waits on it, so it takes Clock rather than Timed.
package session

import (
	coresession "github.com/kitsunium/sdk/internal/core/session"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	//: the sealer resolves "aes-256-gcm" through the core AEAD registry, and a
	//: scheme is only in that registry once its package has been imported.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
)

// wrapAs returns the given session sentinel as the error ORIGIN — its code,
// reason, public and private win under errs' origin-wins rule — with the
// cause's message carried as a structured field.
//
// Attaching the cause as a field rather than as the wrap origin is the choice
// the package makes everywhere: a filesystem error must not be able to hijack
// the code a framework routes on, and the uniform typed shape (401 for every
// "no usable session", 503 + EX_TEMPFAIL for a backend fault) is worth more at
// the call site than errors.Is against fs.ErrPermission. The OS message stays
// reachable through errs.FieldsOf. It never contains the identifier: the file
// store names its files by ID.Digest, which is not a secret.
func wrapAs(sentinel *kerrs.Error, cause error, fields ...kerrs.FieldValue) error {
	//: a nil cause carries no extra field.
	if cause == nil {
		//: still wrapped, so the caller sees one consistent shape.
		return kerrs.Wrap(sentinel, kerrs.WrapParams{}, fields...)
	}
	//: origin-wins keeps the sentinel's identity; the cause is diagnostic.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{},
		append(fields, kerrs.String("cause", cause.Error()))...)
}

// keep the core import referenced even when a source file is built alone.
var _ = coresession.NotFound
