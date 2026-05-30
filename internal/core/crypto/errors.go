// Package crypto — declares the sentinels returned by the AEAD facade. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// CodeDuplicateRegistration is surfaced via panic at boot (see registry.go),
// not as an *Error sentinel.
package crypto

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR — a bad key or undecryptable box is a
// data problem, not a generic internal software error (70).
const exitDataErr int = 65

// httpBadRequest is the HTTP status a decryption failure maps to: the supplied
// ciphertext is client-bad input, not a server fault.
const httpBadRequest int = 400

var (
	// UnknownAlgorithm is returned by Seal/SealAs when no AEAD is registered
	// under the requested Algorithm — typically a missing blank-import.
	UnknownAlgorithm = errs.Define(CodeUnknownAlgorithm, "UNKNOWN_ALGORITHM",
		"No AEAD is registered under that algorithm",
		"core/crypto.Seal: algorithm absent from registry; blank-import the scheme's package to register it")

	// InvalidKey is returned by NewKey when the supplied material is not
	// exactly KeyLen bytes (or by an AEAD when the cipher rejects the key).
	InvalidKey = errs.Define(CodeInvalidKey, "INVALID_KEY",
		"Encryption key has the wrong length",
		"core/crypto.NewKey: key must be exactly KeyLen bytes",
		errs.WithExitCode(exitDataErr))

	// DecryptionFailed is the single non-oracle error returned by Open for any
	// failure — bad tag, malformed framing, unknown wire id, or wrong key.
	DecryptionFailed = errs.Define(CodeDecryptionFailed, "DECRYPTION_FAILED",
		"Decryption failed",
		"core/crypto.Open: authentication, framing, or key check failed (cause withheld to avoid an oracle)",
		errs.WithHTTPStatus(httpBadRequest),
		errs.WithExitCode(exitDataErr))

	// EntropyFailed wraps a crypto/rand failure while generating a Seal nonce;
	// it signals a host entropy fault rather than a caller error.
	EntropyFailed = errs.Define(CodeEntropyFailed, "ENTROPY_FAILED",
		"Could not gather entropy for encryption",
		"core/crypto.Seal: crypto/rand.Read failed while generating a nonce")

	// UnknownHashAlgorithm is returned by Sum/SumHex when no Hasher is
	// registered under the requested Algorithm — typically a missing blank-import.
	UnknownHashAlgorithm = errs.Define(CodeUnknownHashAlgorithm, "UNKNOWN_HASH_ALGORITHM",
		"No hasher is registered under that algorithm",
		"core/crypto.Sum: hash algorithm absent from registry; blank-import the hasher's package to register it")
)
