// Package checks — crypto domain. This file exercises the pkg/v1 crypto facades
// (AEAD, hash, sign, kdf, password, mac, agree) and asserts each round-trip
// property; the facades are pure-Go and identical on every OS, so no build tags.
package checks

import (
	"bytes"
	"fmt"

	agree "github.com/kitsunium/sdk/pkg/v1/agree"
	crypto "github.com/kitsunium/sdk/pkg/v1/crypto"
	hash "github.com/kitsunium/sdk/pkg/v1/hash"
	kdf "github.com/kitsunium/sdk/pkg/v1/kdf"
	mac "github.com/kitsunium/sdk/pkg/v1/mac"
	password "github.com/kitsunium/sdk/pkg/v1/password"
	sign "github.com/kitsunium/sdk/pkg/v1/sign"

	"github.com/kitsunium/sdk/e2e/harness"
)

// cryptoDomain labels every crypto conformance Result.
const cryptoDomain string = "crypto"

// sha256HexLen is the hex-string length of a SHA-256 digest (digest bytes × 2).
const sha256HexLen int = 64

// cryptoSubkeyLen is the byte length the KDF and agreement checks derive.
const cryptoSubkeyLen int = 32

// Check names, one per crypto conformance behaviour.
const (
	// nameAEADRoundTrip names the AEAD seal/open round-trip check.
	nameAEADRoundTrip string = "aead/roundtrip"
	// nameAEADWrongAAD names the aad-binding rejection check.
	nameAEADWrongAAD string = "aead/wrong-aad"
	// nameHash names the deterministic SHA-256 hashing check.
	nameHash string = "hash/sha256-deterministic"
	// nameSign names the Ed25519 sign/verify check.
	nameSign string = "sign/ed25519"
	// nameKDF names the HKDF subkey-derivation check.
	nameKDF string = "kdf/hkdf-subkey"
	// namePassword names the PBKDF2 password hash/verify check.
	namePassword string = "password/pbkdf2"
	// nameMAC names the HMAC-SHA256 tag/verify check.
	nameMAC string = "mac/hmac-sha256"
	// nameAgree names the X25519 key-agreement check.
	nameAgree string = "agree/x25519"
)

// cryptoKey32 is a fixed crypto.KeyLen-byte key for the symmetric checks. A
// deterministic key keeps the conformance run reproducible; it is test-only
// material, never a real secret.
var cryptoKey32 = []byte("0123456789abcdef0123456789abcdef")

// Crypto returns the crypto-domain conformance checks (AEAD seal/open, hashing,
// signing, key derivation, password hashing, MAC, key agreement).
func Crypto() harness.Suite {
	//: each Check exercises one facade verb-set and asserts its round-trip.
	checks := []harness.Check{
		cryptoAEADRoundTrip,
		cryptoAEADWrongAAD,
		cryptoHashDeterministic,
		cryptoSignVerify,
		cryptoKDFDeterministic,
		cryptoPasswordRoundTrip,
		cryptoMACRoundTrip,
		cryptoAgreeSharedKey,
	}
	//: bundle the eight checks under the crypto domain label.
	return harness.Suite{Domain: cryptoDomain, Checks: checks}
}

// cryptoAEADRoundTrip asserts crypto.Seal then crypto.Open recovers the exact
// plaintext under a matching key and aad.
func cryptoAEADRoundTrip() harness.Result {
	//: the plaintext and bound associated data for this round-trip.
	plaintext := []byte("attack at dawn")
	aad := []byte("record-42")
	//: NewKey enforces the 32-byte length and copies defensively.
	key, kErr := crypto.NewKey(cryptoKey32)
	//: a key-construction fault means the symmetric surface is unusable here.
	if kErr != nil {
		//: surface the observed error so the failure is diagnosable.
		return harness.Failed(cryptoDomain, nameAEADRoundTrip, fmt.Sprintf("NewKey: %v", kErr))
	}
	//: Seal generates and embeds the nonce, returning a self-describing box.
	box, sErr := crypto.Seal(key, plaintext, aad)
	//: a seal fault means the default AES-256-GCM scheme did not activate.
	if sErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAEADRoundTrip, fmt.Sprintf("Seal: %v", sErr))
	}
	//: Open strips the nonce, verifies the aad, and returns the plaintext.
	got, oErr := crypto.Open(key, box, aad)
	//: an open fault under the correct key+aad is a conformance failure.
	if oErr != nil {
		//: surface the observed error and the box size.
		return harness.Failed(cryptoDomain, nameAEADRoundTrip, fmt.Sprintf("Open (%d-byte box): %v", len(box), oErr))
	}
	//: the round-trip property: opened plaintext equals the sealed plaintext.
	if !bytes.Equal(plaintext, got) {
		//: show both sides so the corruption is diagnosable.
		return harness.Failed(cryptoDomain, nameAEADRoundTrip, fmt.Sprintf("plaintext mismatch: want %q got %q", plaintext, got))
	}
	//: a correct seal→open round-trip recovering the exact plaintext.
	return harness.Passed(cryptoDomain, nameAEADRoundTrip, fmt.Sprintf("%d-byte box opened to %q", len(box), got))
}

// cryptoAEADWrongAAD asserts crypto.Open rejects a box when the aad differs from
// what it was sealed under — the tag binds the aad.
func cryptoAEADWrongAAD() harness.Result {
	//: seal under one aad, then attempt to open under a different aad.
	plaintext := []byte("attack at dawn")
	sealAAD := []byte("record-42")
	openAAD := []byte("record-99")
	//: NewKey enforces the 32-byte length and copies defensively.
	key, kErr := crypto.NewKey(cryptoKey32)
	//: a key-construction fault means the symmetric surface is unusable here.
	if kErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAEADWrongAAD, fmt.Sprintf("NewKey: %v", kErr))
	}
	//: Seal binds sealAAD into the authentication tag.
	box, sErr := crypto.Seal(key, plaintext, sealAAD)
	//: a seal fault means the default scheme did not activate.
	if sErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAEADWrongAAD, fmt.Sprintf("Seal: %v", sErr))
	}
	//: Open under a mismatched aad MUST fail — the tag no longer verifies.
	_, oErr := crypto.Open(key, box, openAAD)
	//: a nil error here means the aad binding was not enforced — a real failure.
	if oErr == nil {
		//: report that Open wrongly succeeded under the wrong aad.
		return harness.Failed(cryptoDomain, nameAEADWrongAAD, "Open succeeded under wrong aad (tag binding not enforced)")
	}
	//: the expected behaviour: Open rejects the box under the wrong aad.
	return harness.Passed(cryptoDomain, nameAEADWrongAAD, fmt.Sprintf("Open rejected wrong aad: %v", oErr))
}

// cryptoHashDeterministic asserts hash.SumHex is deterministic for one input and
// produces a digest of the algorithm's expected length.
func cryptoHashDeterministic() harness.Result {
	//: a fixed input so the digest is reproducible across runs and machines.
	input := []byte("the quick brown fox")
	//: SumHex returns the canonical lowercase-hex content id.
	first, e1 := hash.SumHex(hash.SHA256, input)
	//: a hashing fault means the stdlib hashers did not activate.
	if e1 != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameHash, fmt.Sprintf("SumHex #1: %v", e1))
	}
	//: hash the same input again to assert determinism.
	second, e2 := hash.SumHex(hash.SHA256, input)
	//: a fault on the second call is equally a conformance failure.
	if e2 != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameHash, fmt.Sprintf("SumHex #2: %v", e2))
	}
	//: determinism: the same input must yield the identical digest.
	if first != second {
		//: show both digests so the non-determinism is diagnosable.
		return harness.Failed(cryptoDomain, nameHash, fmt.Sprintf("non-deterministic: %s != %s", first, second))
	}
	//: a SHA-256 hex digest is exactly 64 characters — wrong length is a fault.
	if len(first) != sha256HexLen {
		//: report the observed length so the gap is visible.
		return harness.Failed(cryptoDomain, nameHash, fmt.Sprintf("digest length %d, want %d (%s)", len(first), sha256HexLen, first))
	}
	//: a deterministic, correctly-sized SHA-256 content id.
	return harness.Passed(cryptoDomain, nameHash, fmt.Sprintf("sha256=%s", first))
}

// cryptoSignVerify asserts a fresh keypair signs a message and verifies true,
// while a tampered message verifies false.
func cryptoSignVerify() harness.Result {
	//: the message to sign and a tampered copy that must not verify.
	message := []byte("transfer 100 to bob")
	tampered := []byte("transfer 999 to bob")
	//: GenerateKey draws a fresh Ed25519 keypair.
	pub, priv, gErr := sign.GenerateKey(sign.Ed25519)
	//: a key-generation fault means the Ed25519 signer did not activate.
	if gErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameSign, fmt.Sprintf("GenerateKey: %v", gErr))
	}
	//: Sign produces a detached signature over the message under priv.
	sig, sErr := sign.Sign(sign.Ed25519, priv, message)
	//: a signing fault means a malformed priv or unregistered scheme.
	if sErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameSign, fmt.Sprintf("Sign: %v", sErr))
	}
	//: Verify must accept the genuine signature over the original message.
	ok, vErr := sign.Verify(sign.Ed25519, pub, message, sig)
	//: an error here signals scheme misconfiguration, not an invalid signature.
	if vErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameSign, fmt.Sprintf("Verify: %v", vErr))
	}
	//: a genuine signature over the original message must verify true.
	if !ok {
		//: report the false verdict on a valid signature.
		return harness.Failed(cryptoDomain, nameSign, fmt.Sprintf("valid signature verified false (%d-byte sig)", len(sig)))
	}
	//: Verify over a tampered message must report false (invalid is (false,nil)).
	bad, bErr := sign.Verify(sign.Ed25519, pub, tampered, sig)
	//: an error here signals scheme misconfiguration, not an invalid signature.
	if bErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameSign, fmt.Sprintf("Verify(tampered): %v", bErr))
	}
	//: a tampered message must NOT verify — a true verdict is a real failure.
	if bad {
		//: report that the tampered message wrongly verified.
		return harness.Failed(cryptoDomain, nameSign, "tampered message verified true")
	}
	//: valid verifies true, tampered verifies false — the signature contract.
	return harness.Passed(cryptoDomain, nameSign, fmt.Sprintf("%d-byte sig: valid=true tampered=false", len(sig)))
}

// cryptoKDFDeterministic asserts kdf.Subkey is deterministic for identical
// inputs yet diverges when the info label (context) differs.
func cryptoKDFDeterministic() harness.Result {
	//: the high-entropy master secret and optional domain randomness.
	secret := []byte("a-32-byte-high-entropy-secret-ok")
	salt := []byte("salt-value")
	//: derive twice under the SAME info to assert determinism.
	a1, e1 := kdf.Subkey(kdf.HKDFSHA256, secret, salt, "aead-key", cryptoSubkeyLen)
	//: a derivation fault means HKDF-SHA256 did not activate.
	if e1 != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameKDF, fmt.Sprintf("Subkey aead-key #1: %v", e1))
	}
	//: the second derivation with identical inputs must reproduce the subkey.
	a2, e2 := kdf.Subkey(kdf.HKDFSHA256, secret, salt, "aead-key", cryptoSubkeyLen)
	//: a fault on the repeat derivation is a conformance failure.
	if e2 != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameKDF, fmt.Sprintf("Subkey aead-key #2: %v", e2))
	}
	//: determinism: identical inputs must yield byte-identical subkeys.
	if !bytes.Equal(a1, a2) {
		//: report the divergence on identical inputs.
		return harness.Failed(cryptoDomain, nameKDF, "same inputs derived different subkeys")
	}
	//: derive under a DIFFERENT info label to assert context separation.
	b, e3 := kdf.Subkey(kdf.HKDFSHA256, secret, salt, "mac-key", cryptoSubkeyLen)
	//: a derivation fault on the second context is a conformance failure.
	if e3 != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameKDF, fmt.Sprintf("Subkey mac-key: %v", e3))
	}
	//: key separation: a different info label must derive a different subkey.
	if bytes.Equal(a1, b) {
		//: report that distinct contexts collided — separation is broken.
		return harness.Failed(cryptoDomain, nameKDF, "different info labels derived the same subkey")
	}
	//: deterministic per context, independent across contexts — the KDF contract.
	return harness.Passed(cryptoDomain, nameKDF, fmt.Sprintf("%d-byte subkey deterministic per-context, distinct across contexts", len(a1)))
}

// cryptoPasswordRoundTrip asserts password.Hash then Verify accepts the right
// password and rejects a wrong one.
func cryptoPasswordRoundTrip() harness.Result {
	//: the stored password and a wrong guess that must be rejected.
	pw := []byte("correct horse battery staple")
	wrong := []byte("Tr0ub4dor&3")
	//: Hash produces a salted, self-describing PHC string for the password.
	phc, hErr := password.Hash(password.PBKDF2SHA256, pw)
	//: a hashing fault means PBKDF2-SHA256 did not activate.
	if hErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, namePassword, fmt.Sprintf("Hash: %v", hErr))
	}
	//: a PHC string is required to verify against — an empty one is a fault.
	if len(phc) == 0 {
		//: report the empty PHC.
		return harness.Failed(cryptoDomain, namePassword, "Hash produced an empty PHC string")
	}
	//: Verify must accept the correct password against the stored hash.
	ok, vErr := password.Verify(pw, phc)
	//: an error here signals a malformed PHC or unregistered scheme.
	if vErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, namePassword, fmt.Sprintf("Verify(correct): %v", vErr))
	}
	//: the correct password must match its stored hash.
	if !ok {
		//: report that the right password was wrongly rejected.
		return harness.Failed(cryptoDomain, namePassword, "correct password verified false")
	}
	//: Verify must reject a wrong password (a mismatch is (false, nil)).
	bad, bErr := password.Verify(wrong, phc)
	//: an error here signals a malformed PHC, not a mismatch.
	if bErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, namePassword, fmt.Sprintf("Verify(wrong): %v", bErr))
	}
	//: a wrong password must NOT match — a true verdict is a real failure.
	if bad {
		//: report that the wrong password wrongly matched.
		return harness.Failed(cryptoDomain, namePassword, "wrong password verified true")
	}
	//: correct accepts, wrong rejects — the password-storage contract.
	return harness.Passed(cryptoDomain, namePassword, fmt.Sprintf("correct=true wrong=false (%d-char PHC)", len(phc)))
}

// cryptoMACRoundTrip asserts mac.Tag then Verify accepts the genuine tag and
// rejects a tampered message under the same key.
func cryptoMACRoundTrip() harness.Result {
	//: the message to tag and a tampered copy that must fail verification.
	message := []byte("ship it")
	tampered := []byte("ship it!")
	//: NewKey enforces the 32-byte length and copies defensively.
	key, kErr := mac.NewKey(cryptoKey32)
	//: a key-construction fault means the MAC surface is unusable here.
	if kErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameMAC, fmt.Sprintf("NewKey: %v", kErr))
	}
	//: Tag produces the authentication tag over the message under key.
	tag, tErr := mac.Tag(mac.HMACSHA256, key, message)
	//: a tagging fault means HMAC-SHA256 did not activate.
	if tErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameMAC, fmt.Sprintf("Tag: %v", tErr))
	}
	//: Verify must accept the genuine tag (constant-time comparison).
	ok, vErr := mac.Verify(mac.HMACSHA256, key, message, tag)
	//: an error here signals an unregistered scheme, not a bad tag.
	if vErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameMAC, fmt.Sprintf("Verify: %v", vErr))
	}
	//: the genuine tag over the original message must verify true.
	if !ok {
		//: report the false verdict on a valid tag.
		return harness.Failed(cryptoDomain, nameMAC, fmt.Sprintf("valid tag verified false (%d-byte tag)", len(tag)))
	}
	//: Verify over a tampered message must report false.
	bad, bErr := mac.Verify(mac.HMACSHA256, key, tampered, tag)
	//: an error here signals an unregistered scheme, not a bad tag.
	if bErr != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameMAC, fmt.Sprintf("Verify(tampered): %v", bErr))
	}
	//: a tampered message must NOT verify — a true verdict is a real failure.
	if bad {
		//: report that the tampered message wrongly verified.
		return harness.Failed(cryptoDomain, nameMAC, "tampered message verified true")
	}
	//: genuine verifies true, tampered verifies false — the MAC contract.
	return harness.Passed(cryptoDomain, nameMAC, fmt.Sprintf("%d-byte tag: valid=true tampered=false", len(tag)))
}

// cryptoAgreeSharedKey asserts two X25519 parties derive the identical shared
// key from each other's public key, and that a different info label diverges.
func cryptoAgreeSharedKey() harness.Result {
	//: party A draws a fresh X25519 keypair.
	pubA, privA, eA := agree.GenerateKey(agree.X25519)
	//: a key-generation fault means the X25519 scheme did not activate.
	if eA != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAgree, fmt.Sprintf("GenerateKey A: %v", eA))
	}
	//: party B draws an independent X25519 keypair.
	pubB, privB, eB := agree.GenerateKey(agree.X25519)
	//: a key-generation fault for B is equally a conformance failure.
	if eB != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAgree, fmt.Sprintf("GenerateKey B: %v", eB))
	}
	//: A derives the shared key from its priv and B's pub under one info label.
	keyA, sA := agree.SharedKey(agree.X25519, privA, pubB, "app-v1")
	//: an agreement fault means a low-order peer point or unregistered scheme.
	if sA != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAgree, fmt.Sprintf("SharedKey A: %v", sA))
	}
	//: B derives the shared key from its priv and A's pub under the same label.
	keyB, sB := agree.SharedKey(agree.X25519, privB, pubA, "app-v1")
	//: an agreement fault for B is equally a conformance failure.
	if sB != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAgree, fmt.Sprintf("SharedKey B: %v", sB))
	}
	//: the agreement property: both parties must arrive at the identical key.
	//: Key is opaque/redacting — Bytes() is the only comparable handle on it.
	if !bytes.Equal(keyA.Bytes(), keyB.Bytes()) {
		//: the key bytes differ — domain separation aside, agreement is broken.
		return harness.Failed(cryptoDomain, nameAgree, "A and B derived different shared keys under the same info")
	}
	//: a different info label must derive a different shared key (domain sep).
	keyC, sC := agree.SharedKey(agree.X25519, privA, pubB, "app-v2")
	//: an agreement fault on the second label is a conformance failure.
	if sC != nil {
		//: surface the observed error.
		return harness.Failed(cryptoDomain, nameAgree, fmt.Sprintf("SharedKey A app-v2: %v", sC))
	}
	//: key separation: the same keypair under a new label must derive a new key.
	if bytes.Equal(keyA.Bytes(), keyC.Bytes()) {
		//: report that distinct info labels collided.
		return harness.Failed(cryptoDomain, nameAgree, "different info labels derived the same shared key")
	}
	//: both parties agree under one label, diverge across labels — the contract.
	return harness.Passed(cryptoDomain, nameAgree, "A==B under app-v1, A!=C under app-v2")
}
