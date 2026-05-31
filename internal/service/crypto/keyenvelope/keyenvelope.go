// Package keyenvelope wraps a symmetric DEK at rest under a passphrase.
//
// A wrapped key is rendered in the frozen PHC-style grammar
//
//	$kenv$v=1$kdf=<id>$<b64salt>$aead=<id>$<b64box>
//
// where <b64salt> and <b64box> use base64.RawStdEncoding (PHC convention).
// The KEK is derived from the passphrase with PBKDF2-SHA256 (600000
// iterations, pinned by the kdf id — there is no iterations field) and the
// DEK is sealed under AES-256-GCM with the envelope header as AAD, binding
// the framing so a tampered header fails the AEAD open. The package mints no
// error codes of its own: structural faults map to the core
// InvalidKeyEnvelope sentinel and a wrong passphrase forwards the core
// DecryptionFailed sentinel verbatim (no key/password oracle beyond
// structural validity).
package keyenvelope

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
)

// kdfID is the only key-derivation id shipped now; it pins the cost params.
const kdfID string = "pbkdf2-sha256"

// aeadID is the only AEAD id shipped now.
const aeadID corecrypto.Algorithm = "aes-256-gcm"

// version is the wire grammar version literal.
const version string = "v=1"

// magic is the leading envelope tag.
const magic string = "kenv"

// iterations is the fixed PBKDF2 work factor pinned to kdfID.
const iterations int = 600000

// saltLen is the salt length in bytes generated per envelope.
const saltLen int = 32

// fieldCount is the number of segments in a well-formed envelope after
// splitting on "$" (the leading empty segment is included).
const fieldCount int = 7

// Field indices into the "$"-split envelope string. The leading empty segment
// is index 0, so the first meaningful field is index 1.
const (
	fieldMagic int = iota + 1
	fieldVer
	fieldKDF
	fieldSalt
	fieldAEAD
	fieldBox
)

// WrapKey seals dek under a KEK derived from passphrase, returning the frozen
// envelope string and an err that is non-nil on entropy or seal failure. The
// caller-owned dek is never zeroized; the transient KEK is wiped on every path.
func WrapKey(passphrase []byte, dek corecrypto.Key) (envelope string, err error) {
	var salt [saltLen]byte
	//: a crypto/rand failure is an entropy fault, not an envelope fault
	if _, rerr := rand.Read(salt[:]); rerr != nil {
		//: wrap the cause as the typed EntropyFailed sentinel
		return "", errs.Wrap(rerr, errs.WrapParams{
			Code:    corecrypto.CodeEntropyFailed,
			Reason:  "ENTROPY_FAILED",
			Public:  "Could not gather entropy for encryption",
			Private: "service/crypto/keyenvelope.WrapKey: crypto/rand.Read failed while generating a salt",
		})
	}
	kek, err := deriveKEK(passphrase, salt[:])
	//: a KEK derivation fault forwards the core sentinel verbatim
	if err != nil {
		//: surface the derivation fault to the caller
		return "", err
	}
	//: the KEK is transient secret material — wipe it on every exit
	defer kek.Zeroize()
	aad := header(base64.RawStdEncoding.EncodeToString(salt[:]))
	box, err := corecrypto.Seal(aeadID, kek, dek.Bytes(), aad)
	//: a seal failure forwards the core sentinel verbatim
	if err != nil {
		//: surface the seal fault to the caller
		return "", err
	}
	//: concatenate the AAD header with the base64 box to form the envelope
	return string(aad) + base64.RawStdEncoding.EncodeToString(box), nil
}

// UnwrapKey parses envelope and opens the DEK using a KEK derived from
// passphrase, returning the recovered dek and an err. A structurally invalid
// envelope yields InvalidKeyEnvelope in err; a wrong passphrase forwards the
// core DecryptionFailed sentinel.
func UnwrapKey(passphrase []byte, envelope string) (dek corecrypto.Key, err error) {
	salt, box, err := parseEnvelope(envelope)
	//: a structurally corrupt envelope cannot be unwrapped
	if err != nil {
		//: surface the structural fault to the caller
		return corecrypto.Key{}, err
	}
	kek, err := deriveKEK(passphrase, salt)
	//: a KEK derivation fault forwards the core sentinel verbatim
	if err != nil {
		//: surface the derivation fault to the caller
		return corecrypto.Key{}, err
	}
	//: the KEK is transient secret material — wipe it on every exit
	defer kek.Zeroize()
	aad := header(base64.RawStdEncoding.EncodeToString(salt))
	raw, err := corecrypto.Open(kek, box, aad)
	//: an open failure means wrong passphrase or tampered box — forward it
	if err != nil {
		//: surface the non-oracle decryption failure to the caller
		return corecrypto.Key{}, err
	}
	//: the recovered plaintext bytes are transient — wipe after copy into Key
	defer clear(raw)
	//: wrap the recovered DEK bytes into a redacting Key
	return corecrypto.NewKey(raw)
}

// deriveKEK derives the KEK from passphrase and salt via stdlib PBKDF2-SHA256,
// returning the wrapped Key and any derivation error (unreachable with our
// pinned, valid parameters). The intermediate plaintext is wiped before return.
func deriveKEK(passphrase, salt []byte) (kek corecrypto.Key, err error) {
	raw, kerr := pbkdf2.Key(sha256.New, string(passphrase), salt, iterations, corecrypto.KeyLen)
	//: PBKDF2 only errors on invalid params (unreachable with our constants)
	if kerr != nil {
		//: surface a typed sentinel rather than a raw error
		return corecrypto.Key{}, corecrypto.EntropyFailed
	}
	//: the intermediate KEK plaintext is transient — wipe after copy into Key
	defer clear(raw)
	//: wrap the derived bytes into a redacting Key
	return corecrypto.NewKey(raw)
}

// header renders the envelope header (everything up to and including the box
// separator); it doubles as the AEAD AAD so a tampered header fails the open.
func header(b64salt string) []byte {
	var b strings.Builder
	b.WriteString("$")
	b.WriteString(magic)
	b.WriteString("$")
	b.WriteString(version)
	b.WriteString("$kdf=")
	b.WriteString(kdfID)
	b.WriteString("$")
	b.WriteString(b64salt)
	b.WriteString("$aead=")
	b.WriteString(string(aeadID))
	b.WriteString("$")
	//: the rendered header bytes double as the AEAD AAD
	return []byte(b.String())
}

// parseEnvelope decodes envelope into its salt and sealed box, validating the
// frozen grammar; any structural fault yields InvalidKeyEnvelope.
func parseEnvelope(envelope string) (salt, box []byte, err error) {
	parts := strings.Split(envelope, "$")
	//: the framing fields (count, magic, version, ids) must all match exactly
	if !validFraming(parts) {
		//: a malformed frame is structural corruption
		return nil, nil, corecrypto.InvalidKeyEnvelope
	}
	salt, serr := base64.RawStdEncoding.DecodeString(parts[fieldSalt])
	//: corrupt salt base64 or wrong salt length makes the envelope unusable
	if serr != nil || len(salt) != saltLen {
		//: an unusable salt is structural corruption
		return nil, nil, corecrypto.InvalidKeyEnvelope
	}
	box, berr := base64.RawStdEncoding.DecodeString(parts[fieldBox])
	//: corrupt box base64 makes the envelope unusable
	if berr != nil {
		//: an unusable box is structural corruption
		return nil, nil, corecrypto.InvalidKeyEnvelope
	}
	//: a fully validated envelope yields its salt and sealed box
	return salt, box, nil
}

// validFraming reports whether the "$"-split parts carry the frozen segment
// count, magic, version, and the only shipped kdf and aead ids.
func validFraming(parts []string) bool {
	//: an exact segment count, magic, and version are mandatory
	if len(parts) != fieldCount || parts[0] != "" || parts[fieldMagic] != magic || parts[fieldVer] != version {
		//: any framing deviation rejects the envelope
		return false
	}
	//: the kdf and aead ids must match the shipped ids exactly
	return parts[fieldKDF] == "kdf="+kdfID && parts[fieldAEAD] == "aead="+string(aeadID)
}
