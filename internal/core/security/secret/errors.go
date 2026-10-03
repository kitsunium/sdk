// Package secret — declares the sentinel *errs.Error outcomes: the verdicts of
// the ports, and the refusals of the concrete stores, the keyring, the rotator
// and the subject keys in internal/service/security/secret (ADR 0160). Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private here carries a secret, and no field ever will: the
// whole domain exists so that a value can travel through a program without
// being written down by accident, and an error message is the most-written
// text a program produces. A secret's NAME is not secret and may travel as a
// log-only field, because an operator cannot act on "a secret is missing"
// without knowing which one.
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A missing secret, a malformed
// name and a write to a read-only store are all wiring faults: the same call
// is refused identically forever and the fix is in the deployment or at the
// call site, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may answer the next attempt, which is exactly what distinguishes
// StoreUnavailable from every other verdict in this block.
const exitTempFail int = 75

// httpUnavailable is 503: the store, not the request, is the problem.
const httpUnavailable int = 503

var (
	// NotFound is returned by Get, Versions and Prune for a name that holds
	// no version — never written to a writable store, or supplied by nobody
	// to a read-only one.
	NotFound = errs.Define(CodeNotFound, "NOT_FOUND",
		"No secret has that name",
		"core/security/secret: the store holds no version under the requested name; the field names it, the value never existed",
		errs.WithExitCode(exitConfig))

	// InvalidName is returned by every Store method, and by ValidateName, for
	// a name outside the grammar. The grammar is closed on purpose: a name
	// becomes a file name in one store and a variable name in another, and a
	// character one of them treats specially would make two stores disagree
	// about which secret a name designates.
	InvalidName = errs.Define(CodeInvalidName, "INVALID_NAME",
		"That is not a valid secret name",
		"core/security/secret: a name must be 1-63 characters of a-z, 0-9 and '-', starting and ending with a letter or a digit",
		errs.WithExitCode(exitConfig))

	// ReadOnly is returned by Put and Prune on a store that only reads. The
	// environment is the shipped example: the process did not write its own
	// environment and cannot rotate what an orchestrator mounted.
	ReadOnly = errs.Define(CodeReadOnly, "READ_ONLY",
		"This secret store cannot be written",
		"core/security/secret: Put or Prune was called on a store that only reads — rotate at the source that supplies it",
		errs.WithExitCode(exitConfig))

	// StoreUnavailable is returned when the backend itself failed. It carries
	// EX_TEMPFAIL and 503 because a retry is meaningful, which is what
	// separates it from every other verdict in this block.
	StoreUnavailable = errs.Define(CodeStoreUnavailable, "STORE_UNAVAILABLE",
		"The secret store is unavailable",
		"core/security/secret: the backend could not be read or written, or its lock could not be taken; the fields name the operation and the secret, never a value",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// ValueRefused is returned by Value.UnmarshalJSON and Value.UnmarshalText.
	//
	// Two inputs are refused. A JSON token that is not a string: a number has
	// already been re-spelled on its way to the decoder — 1e3 arrives as 1000
	// and a twenty-digit token loses its tail to float64 — so accepting it
	// would store a secret the operator never wrote. And the redaction
	// placeholder itself: it is what every rendering of a Value writes, so
	// finding it on the way IN means a rendered configuration was fed back as
	// a real one, and every secret in it would silently become the placeholder.
	ValueRefused = errs.Define(CodeValueRefused, "VALUE_REFUSED",
		"A secret can only be decoded from a string, and never from its own placeholder",
		"core/security/secret: the decoder was given a non-string JSON token or the redaction placeholder; quote the secret, and never reload a rendered configuration",
		errs.WithExitCode(exitConfig))

	// EmptyValue is returned by Put for an empty secret. Storing one would
	// turn an unfilled field into a version every reader then trusts (ADR
	// 0031: a zero value is refused where no default is safe, and no secret
	// is a safe default).
	EmptyValue = errs.Define(CodeEmptyValue, "EMPTY_VALUE",
		"An empty secret cannot be stored",
		"core/security/secret: Put was given a zero Value; an empty secret is what an unfilled field looks like",
		errs.WithExitCode(exitConfig))

	// InvalidKeep is returned by Prune when asked to keep fewer than one
	// version. Keeping none would delete the secret, and deletion is a
	// different decision from pruning — one a rotation must never take by
	// arithmetic accident.
	InvalidKeep = errs.Define(CodeInvalidKeep, "INVALID_KEEP",
		"A prune must keep at least one version",
		"core/security/secret: Prune was asked to keep fewer than one version, which would delete the secret rather than prune it",
		errs.WithExitCode(exitConfig))

	// InvalidSubject is returned by ValidateSubject, and by every call that
	// takes a subject, for a subject outside the grammar (ADR 0142). The
	// rejected string is never repeated: the one most likely to be refused is
	// the identity a caller forgot to derive a reference from.
	InvalidSubject = errs.Define(CodeInvalidSubject, "INVALID_SUBJECT",
		"That is not a valid subject reference",
		"core/security/secret: a subject must be 1-128 bytes of a-z, 0-9, '-', '_', '.' and ':', starting with a letter or a digit",
		errs.WithExitCode(exitConfig))

	// The engines' own refusals (0.3.68.*). They are raised by
	// internal/service/security/secret — their Private names that package, the
	// one that raises them — and declared here since ADR 0160, so every code of
	// the domain is in one place.
	//
	// No Public, Private or field here carries a secret, a data key or a sealed
	// box either. A secret's name, a version number, an environment variable's
	// NAME, a valid subject reference and an operation may travel as log-only
	// fields; a file path does not, because the directory a store owns is a
	// deployment detail an error has no reason to repeat.

	// InvalidConfig is returned by every engine constructor for a
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
	// from StoreUnavailable: the likeliest cause is a store opened with a
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
