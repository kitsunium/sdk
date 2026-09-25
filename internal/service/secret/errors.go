// Package secret — declares the sentinel *errs.Error outcomes of the concrete
// stores, the keyring and the rotator. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form.
//
// No Public, Private or field here carries a secret or a sealed box. A
// secret's name, a version number, an environment variable's NAME and an
// operation may travel as log-only fields; a file path does not, because the
// directory a store owns is a deployment detail an error has no reason to
// repeat.
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78): the fix is in the wiring or the
// deployment, and the same call is refused identically until it is made.
const exitConfig int = 78

var (
	// InvalidConfig is returned by every constructor in this package for a
	// configuration it cannot honour (ADR 0031): a missing directory, a
	// directory other accounts can read, a malformed prefix, a nil store, a
	// rotation policy with no interval, fewer than two kept versions, or no
	// generator. The fields name the setting and the clause, never a value.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The secret store configuration is not usable",
		"service/secret: a constructor refused its configuration; the fields name the setting and the clause",
		errs.WithExitCode(exitConfig))

	// RecordUnreadable is returned by the file store for a record it found and
	// could not read back. It is NOT transient, which is what separates it
	// from core StoreUnavailable: the likeliest cause is a store opened with a
	// different key from the one that wrote it, and retrying reads the same
	// bytes with the same key.
	RecordUnreadable = errs.Define(CodeRecordUnreadable, "RECORD_UNREADABLE",
		"A stored secret could not be read back",
		"service/secret: a file-store record is truncated, tampered, sealed under another key, or of an unknown format; the field names the secret",
		errs.WithExitCode(exitConfig))

	// EnvRefused is returned by the environment store when the environment
	// names a secret without supplying a usable value. Setting both NAME and
	// NAME_FILE is refused rather than resolved by a precedence rule, exactly
	// as the official container images refuse it: the operator meant one of
	// them, and picking silently is how the wrong one ends up in production.
	EnvRefused = errs.Define(CodeEnvRefused, "ENV_REFUSED",
		"The environment does not supply a usable value for that secret",
		"service/secret: both the variable and its _FILE form are set, or the _FILE form names an empty or oversized file; the fields name the variables",
		errs.WithExitCode(exitConfig))

	// SealInvalid is returned by Keyring.Open for every failure without
	// distinguishing them: a box too short to carry a header, an unknown
	// format, a version no longer kept, a flipped bit, the wrong associated
	// data. One verdict is what keeps Open from being an oracle — the posture
	// crypto.DecryptionFailed takes for the AEAD underneath.
	SealInvalid = errs.Define(CodeSealInvalid, "SEAL_INVALID",
		"That sealed value could not be opened",
		"service/secret: the box failed to open — malformed, sealed under a version no longer kept, tampered, or bound to other data — deliberately not distinguished")

	// SignatureInvalid is returned by Keyring.Verify for every failure without
	// distinguishing them, for the same reason SealInvalid does.
	SignatureInvalid = errs.Define(CodeSignatureInvalid, "SIGNATURE_INVALID",
		"That signature is not valid",
		"service/secret: the signature failed to verify — malformed, made under a version no longer kept, or over other bytes — deliberately not distinguished")

	// KeyMaterialInvalid is returned by the keyring when the version it must
	// use is not exactly one crypto.Key long. A keyring's versions are keys, and a
	// password stored under a keyring's name is not one: stretching it
	// silently would hide that it was never random.
	KeyMaterialInvalid = errs.Define(CodeKeyMaterialInvalid, "KEY_MATERIAL_INVALID",
		"That secret cannot be used as a key",
		"service/secret: a keyring version is not exactly 32 bytes; generate its versions with Random(32)",
		errs.WithExitCode(exitConfig))

	// GenerateFailed is returned by a rotation whose policy could not produce
	// a new secret. Nothing is stored and nothing is pruned, so the current
	// version stays current.
	GenerateFailed = errs.Define(CodeGenerateFailed, "GENERATE_FAILED",
		"A new secret could not be generated",
		"service/secret: the rotation policy's generator returned an error or an empty value; the current version is unchanged")

	// KeyFileInvalid is returned by KeyFile for a file that exists and does not
	// hold one key: a directory or a device where a file belongs, or content
	// that is not exactly one crypto.Key long. It is never repaired — a key
	// truncated or padded to fit is a different key, and a store sealed under
	// the original would then read as corrupt.
	KeyFileInvalid = errs.Define(CodeKeyFileInvalid, "KEY_FILE_INVALID",
		"The key file does not hold a key",
		"service/secret: the key file is not a regular file or is not exactly 32 raw bytes; the content is never repeated",
		errs.WithExitCode(exitConfig))
)

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
