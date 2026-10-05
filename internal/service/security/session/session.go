package session

import (
	coresession "github.com/kitsunium/sdk/internal/core/security/session"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	//: the sealer resolves "aes-256-gcm" through the core AEAD registry, and a
	//: scheme is only in that registry once its package has been imported.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aead/aesgcm"
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
