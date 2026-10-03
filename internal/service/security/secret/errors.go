// Package secret — declares the sentinel *errs.Error outcomes of the concrete
// stores, the keyring, the rotator and the subject keys. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public, Private or field here carries a secret, a data key or a sealed
// box. A secret's name, a version number, an environment variable's NAME, a
// valid subject reference and an operation may travel as log-only fields; a
// file path does not, because the directory a store owns is a deployment
// detail an error has no reason to repeat.
package secret

import (
	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitConfig matches sysexits EX_CONFIG (78): the fix is in the wiring or the
// deployment, and the same call is refused identically until it is made.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75), the exit status of core
// StoreUnavailable: a subject-key store that failed may answer the next
// attempt, and a verdict built around a plain store error carries it too.
const exitTempFail int = 75

var (
	// InvalidConfig is returned by every constructor in this package for a
	// configuration it cannot honour (ADR 0031): a missing directory, a
	// directory other accounts can read, a malformed prefix, a nil store, a
	// rotation policy with no interval, fewer than two kept versions, or no
	// generator. The fields name the setting and the clause, never a value.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The secret store configuration is not usable",
		"service/security/secret: a constructor refused its configuration; the fields name the setting and the clause",
		errs.WithExitCode(exitConfig))

	// RecordUnreadable is returned by the file store for a record it found and
	// could not read back. It is NOT transient, which is what separates it
	// from core StoreUnavailable: the likeliest cause is a store opened with a
	// different key from the one that wrote it, and retrying reads the same
	// bytes with the same key.
	RecordUnreadable = errs.Define(CodeRecordUnreadable, "RECORD_UNREADABLE",
		"A stored secret could not be read back",
		"service/security/secret: a file-store record is truncated, tampered, sealed under another key, or of an unknown format; the field names the secret",
		errs.WithExitCode(exitConfig))

	// EnvRefused is returned by the environment store when the environment
	// names a secret without supplying a usable value. Setting both NAME and
	// NAME_FILE is refused rather than resolved by a precedence rule, exactly
	// as the official container images refuse it: the operator meant one of
	// them, and picking silently is how the wrong one ends up in production.
	EnvRefused = errs.Define(CodeEnvRefused, "ENV_REFUSED",
		"The environment does not supply a usable value for that secret",
		"service/security/secret: both the variable and its _FILE form are set, or the _FILE form names an empty or oversized file; the fields name the variables",
		errs.WithExitCode(exitConfig))

	// SealInvalid is returned by Keyring.Open for every failure without
	// distinguishing them: a box too short to carry a header, an unknown
	// format, a version no longer kept, a flipped bit, the wrong associated
	// data. One verdict is what keeps Open from being an oracle — the posture
	// crypto.DecryptionFailed takes for the AEAD underneath.
	SealInvalid = errs.Define(CodeSealInvalid, "SEAL_INVALID",
		"That sealed value could not be opened",
		"service/security/secret: the box failed to open — malformed, sealed under a version no longer kept, tampered, or bound to other data — deliberately not distinguished")

	// SignatureInvalid is returned by Keyring.Verify for every failure without
	// distinguishing them, for the same reason SealInvalid does.
	SignatureInvalid = errs.Define(CodeSignatureInvalid, "SIGNATURE_INVALID",
		"That signature is not valid",
		"service/security/secret: the signature failed to verify — malformed, made under a version no longer kept, or over other bytes — deliberately not distinguished")

	// KeyMaterialInvalid is returned by the keyring when the version it must
	// use is not exactly one crypto.Key long. A keyring's versions are keys, and a
	// password stored under a keyring's name is not one: stretching it
	// silently would hide that it was never random.
	KeyMaterialInvalid = errs.Define(CodeKeyMaterialInvalid, "KEY_MATERIAL_INVALID",
		"That secret cannot be used as a key",
		"service/security/secret: a keyring version is not exactly 32 bytes; generate its versions with Random(32)",
		errs.WithExitCode(exitConfig))

	// GenerateFailed is returned by a rotation whose policy could not produce
	// a new secret. Nothing is stored and nothing is pruned, so the current
	// version stays current.
	GenerateFailed = errs.Define(CodeGenerateFailed, "GENERATE_FAILED",
		"A new secret could not be generated",
		"service/security/secret: the rotation policy's generator returned an error or an empty value; the current version is unchanged")

	// KeyFileInvalid is returned by KeyFile for a file that exists and does not
	// hold one key: a directory or a device where a file belongs, or content
	// that is not exactly one crypto.Key long. It is never repaired — a key
	// truncated or padded to fit is a different key, and a store sealed under
	// the original would then read as corrupt.
	KeyFileInvalid = errs.Define(CodeKeyFileInvalid, "KEY_FILE_INVALID",
		"The key file does not hold a key",
		"service/security/secret: the key file is not a regular file or is not exactly 32 raw bytes; the content is never repeated",
		errs.WithExitCode(exitConfig))

	// KeyDestroyed is returned by SubjectKeys.Open for a box whose data key
	// is not held — the answer an erasure exists to produce, so a caller
	// reading records treats it as "this value was erased" rather than as a
	// fault. It cannot tell a key destroyed from one never made, nor from a
	// box whose key identifier was edited: telling them apart would mean
	// keeping a trace of every erased subject, which is what an erasure
	// removes.
	KeyDestroyed = errs.Define(CodeKeyDestroyed, "KEY_DESTROYED",
		"That value was erased",
		"service/security/secret: the box names a subject key that is not held — destroyed by an erasure, or never made — and nothing can open it again")

	// SubjectKeyUnreadable is returned when a subject's data key is held and
	// does not unwrap under the root keyring. It is NOT an erasure and must
	// never be read as one: the likeliest causes are a root secret replaced by
	// another value under the same version number, and a root version pruned
	// while a key was still wrapped under it — which RotatorConfig.InUse
	// exists to prevent. The engine never replaces such a key; Destroy
	// removes it.
	SubjectKeyUnreadable = errs.Define(CodeSubjectKeyUnreadable, "SUBJECT_KEY_UNREADABLE",
		"A data key could not be unwrapped",
		"service/security/secret: a subject key is held and does not open under the root keyring — its version pruned, the root replaced, or the record altered; the field names the subject",
		errs.WithExitCode(exitConfig))
)

// storeFailure is the verdict for an error a SubjectKeyStore method returned:
// the retryable core StoreUnavailable, naming the operation and — when the
// call was about one — the subject.
//
// It wraps the store's error rather than the sentinel, as the state-machine
// engine does with its own caller's store: a store error that is itself an SDK
// error keeps its code — origin wins — and gains StoreUnavailable in its trail,
// and a plain error stays reachable through errors.Is. A caller's store is the
// caller's code, so its message is the caller's to keep free of secrets; the
// engine adds only a valid subject, which is a reference and never a value.
func storeFailure(cause error, operation, subject string) error {
	fields := []errs.FieldValue{errs.String("operation", operation)}
	//: a call about the whole store names no subject.
	if subject != "" {
		fields = append(fields, errs.String("subject", subject))
	}
	//: origin wins when the store's error is already an SDK error.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coresecret.CodeStoreUnavailable,
		Reason:   "STORE_UNAVAILABLE",
		Public:   coresecret.StoreUnavailable.Public(),
		Private:  coresecret.StoreUnavailable.Private(),
		ExitCode: exitTempFail,
	}, fields...)
}

// wrapAs returns the given sentinel as the error origin — its code, reason and
// public message win — with the cause's message and any extra fields attached
// as log-only metadata.
//
// Wrapping the sentinel rather than the cause is what keeps this domain's
// verdict intact: errs.Wrap is origin-wins, so an *errs.Error cause passed
// first would hijack the code. Callers pass a cause only when its message is
// known not to carry a secret — an I/O error, a lock error — never a decoder's
// message.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field.
	if cause == nil {
		//: the sentinel with the caller's fields only.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: the cause travels as a log-only field.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}
