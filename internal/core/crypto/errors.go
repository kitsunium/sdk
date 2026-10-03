// Package crypto — declares the sentinels returned by the AEAD facade. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// DuplicateRegistration is never returned: every registrar panics at boot with
// conflictText of it (see registry_generic.go and registry.go).
package crypto

import (
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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

	// UnknownSignatureAlgorithm is returned by Sign/Verify/GenerateKey when no
	// Signer is registered under the requested Algorithm — usually a missing
	// blank-import of the scheme's package.
	UnknownSignatureAlgorithm = errs.Define(CodeUnknownSignatureAlgorithm, "UNKNOWN_SIGNATURE_ALGORITHM",
		"No signer is registered under that algorithm",
		"core/crypto.Sign: signature algorithm absent from registry; blank-import the signer's package to register it")

	// SigningFailed is returned by Sign when the supplied private key is not
	// well-formed for the scheme (e.g. the wrong length).
	SigningFailed = errs.Define(CodeSigningFailed, "SIGNING_FAILED",
		"Signing key is malformed",
		"core/crypto.Sign: private key is not valid for the scheme (wrong length or encoding)",
		errs.WithExitCode(exitDataErr))

	// KeyGenerationFailed wraps a crypto/rand failure while generating a signing
	// keypair; it signals a host entropy fault rather than a caller error.
	KeyGenerationFailed = errs.Define(CodeKeyGenerationFailed, "KEY_GENERATION_FAILED",
		"Could not gather entropy for key generation",
		"core/crypto.GenerateKey: crypto/rand failed while generating a signing keypair")

	// UnknownKDFAlgorithm is returned by Subkey when no Deriver is registered
	// under the requested Algorithm — usually a missing blank-import.
	UnknownKDFAlgorithm = errs.Define(CodeUnknownKDFAlgorithm, "UNKNOWN_KDF_ALGORITHM",
		"No key-derivation function is registered under that algorithm",
		"core/crypto.Subkey: KDF algorithm absent from registry; blank-import the deriver's package to register it")

	// DerivationFailed is returned by Subkey when the requested length exceeds
	// the scheme's maximum output (e.g. HKDF's 255*HashLen ceiling).
	DerivationFailed = errs.Define(CodeDerivationFailed, "DERIVATION_FAILED",
		"Requested derived-key length is too large",
		"core/crypto.Subkey: requested length exceeds the scheme's maximum output",
		errs.WithExitCode(exitDataErr))

	// UnknownPasswordAlgorithm is returned by HashPassword/VerifyPassword when no
	// PasswordHasher is registered under the requested Algorithm or PHC id —
	// usually a missing blank-import.
	UnknownPasswordAlgorithm = errs.Define(CodeUnknownPasswordAlgorithm, "UNKNOWN_PASSWORD_ALGORITHM",
		"No password hasher is registered under that algorithm",
		"core/crypto.VerifyPassword: password scheme absent from registry; blank-import the hasher's package to register it")

	// PasswordHashFailed wraps a crypto/rand failure while generating a password
	// salt; it signals a host entropy fault rather than a caller error.
	PasswordHashFailed = errs.Define(CodePasswordHashFailed, "PASSWORD_HASH_FAILED",
		"Could not gather entropy for password hashing",
		"core/crypto.HashPassword: crypto/rand failed while generating a salt")

	// InvalidPasswordHash is returned by VerifyPassword when the stored PHC
	// string cannot be parsed — server-side data corruption, never a mismatch.
	InvalidPasswordHash = errs.Define(CodeInvalidPasswordHash, "INVALID_PASSWORD_HASH",
		"Stored password hash is malformed",
		"core/crypto.VerifyPassword: stored PHC string is not well-formed for its scheme")

	// UnknownMACAlgorithm is returned by MACTag/MACVerify when no MAC is
	// registered under the requested Algorithm — typically a missing blank-import.
	UnknownMACAlgorithm = errs.Define(CodeUnknownMACAlgorithm, "UNKNOWN_MAC_ALGORITHM",
		"No MAC is registered under that algorithm",
		"core/crypto.MACTag: MAC algorithm absent from registry; blank-import the scheme's package to register it")

	// UnknownAgreementAlgorithm is returned by GenerateAgreementKey/AgreementShared
	// when no Agreement scheme is registered under the requested Algorithm.
	UnknownAgreementAlgorithm = errs.Define(CodeUnknownAgreementAlgorithm, "UNKNOWN_AGREEMENT_ALGORITHM",
		"No key-agreement scheme is registered under that algorithm",
		"core/crypto.AgreementShared: agreement algorithm absent from registry; blank-import the scheme's package to register it")

	// AgreementFailed wraps an AgreementShared fault where the scheme rejected the
	// inputs (e.g. a low-order peer point); it never leaks key bytes.
	AgreementFailed = errs.Define(CodeAgreementFailed, "AGREEMENT_FAILED",
		"Key agreement failed to derive a shared secret",
		"core/crypto.AgreementShared: the scheme rejected the inputs (cause withheld of key bytes)",
		errs.WithExitCode(exitDataErr))

	// StreamTruncated is returned by a streaming Open reader when the stream ends
	// before its final-flag chunk — a truncated or tampered stream, not a clean EOF.
	StreamTruncated = errs.Define(CodeStreamTruncated, "STREAM_TRUNCATED",
		"The authenticated stream was truncated before its final chunk",
		"core/crypto.OpenStream: reader hit EOF before decrypting the final-flag chunk",
		errs.WithExitCode(exitDataErr))

	// InvalidKeyEnvelope is returned by UnwrapKey when the supplied $kenv$ string
	// is structurally invalid (bad field count, magic, version, kdf/aead id, or
	// base64). It signals corruption only — a wrong passphrase surfaces the
	// non-oracle DecryptionFailed instead, so this is no key/password oracle.
	InvalidKeyEnvelope = errs.Define(CodeInvalidKeyEnvelope, "INVALID_KEY_ENVELOPE",
		"Key envelope is malformed",
		"service/crypto/key/keyenvelope.UnwrapKey: $kenv$ string is not well-formed",
		errs.WithExitCode(exitDataErr))

	// DigestMismatch is returned by a VerifyingReader on its final (EOF) read when
	// the computed digest does not match the expected value. The digest is public,
	// so the comparison is non-oracle and surfaces only at the terminal read.
	DigestMismatch = errs.Define(CodeDigestMismatch, "DIGEST_MISMATCH",
		"Stream digest does not match the expected value",
		"service/crypto/hash/stdhash.VerifyingReader: computed digest differs from the expected hex at EOF",
		errs.WithExitCode(exitDataErr))

	// DuplicateRegistration is the conflict every registrar refuses at boot: a
	// distinct scheme already holds the Algorithm — or, for an AEAD, the wire
	// id — a second one claims. The registrar panics with conflictText of it,
	// so the panic carries the dotted-quad header AND the key that collided.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A scheme is already registered under that algorithm or wire id",
		"core/crypto: a distinct scheme already holds this Algorithm, or this AEAD wire id")
)

// conflictText renders a registry conflict for the boot panic that reports it:
// the typed error's header and Public, then each field it carries, quoted.
// Error() never renders a field (rule 4), and the panic is the only place an
// operator learns WHICH registrar refused WHICH key.
func conflictText(err error) string {
	var b strings.Builder
	//: the canonical "[0.2.4.1 DUPLICATE_REGISTRATION] <public>" header.
	b.WriteString(err.Error())
	//: then every field, in the order the conflict attached them.
	for i, f := range errs.FieldsOf(err) {
		//: a colon before the first field, a space between the others.
		if i == 0 {
			b.WriteString(":")
		}
		b.WriteString(" " + f.Key() + "=" + strconv.Quote(f.StringValue()))
	}
	//: one line, ready to panic with.
	return b.String()
}
