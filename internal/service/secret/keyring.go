// Package secret — the keyring: the versions of one secret seen as keys.
package secret

import (
	"context"
	"encoding/binary"
	"math"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	// Activates the stdlib AES-256-GCM scheme the keyring and the sealed file
	// store seal with. Stdlib-only.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	// Activates the stdlib HKDF-SHA256 scheme the keyring derives its two
	// purpose-bound subkeys with. Stdlib-only.
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
	// Activates the stdlib HMAC-SHA256 scheme the keyring signs with.
	// Stdlib-only.
	_ "github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
)

// boxFormat is the first byte of every box and every signature a Keyring
// makes. A future format gets another byte, and this one keeps opening.
const boxFormat byte = 0x01

// versionBytes is the width of the version number a box carries, big-endian.
// Four bytes is four billion rotations; one fixed width is one spelling per
// box, which a variable-length integer would not be.
const versionBytes int = 4

// headerLen is the format byte plus the version: what Open reads before it
// knows which key to use.
const headerLen int = 1 + versionBytes

// macAlgorithm signs; kdfAlgorithm separates one version into two keys.
const (
	// macAlgorithm is HMAC-SHA256, the detached-MAC default.
	macAlgorithm corecrypto.Algorithm = "hmac-sha256"
	// kdfAlgorithm is HKDF-SHA256, the key-separation default.
	kdfAlgorithm corecrypto.Algorithm = "hkdf-sha256"
)

// sealLabel and signLabel are the HKDF context labels that make a version's
// sealing key and signing key two independent keys. Using one 32-byte value
// both as an AES key and as an HMAC key is the cross-primitive reuse key
// separation exists to prevent, and the derivation costs a microsecond.
const (
	// sealLabel derives the AEAD key.
	sealLabel string = "kitsunium/secret keyring seal v1"
	// signLabel derives the MAC key.
	signLabel string = "kitsunium/secret keyring sign v1"
)

// bindingPrefix opens the bytes every box's associated data and every
// signature's message are bound to: the keyring's name, NUL-terminated (NUL is
// not in the name alphabet), then the version. A box sealed under keyring "a"
// therefore does not open under keyring "b" even if the two held the same
// bytes, and a box's version cannot be edited in its header.
const bindingPrefix string = "kitsunium/secret keyring v1\x00"

// Keyring sees the versions of ONE secret as keys, so a rotation never breaks
// what was sealed or signed before it:
//
//   - the NEWEST version seals and signs;
//   - EVERY kept version still opens and verifies, because each box and each
//     signature carries the number of the version that made it;
//   - a version stops opening only when it is pruned — the only way to retire
//     a key, and the one a rotation's Keep decides.
//
// A version is key material and must be exactly one crypto.Key long (derived
// from corecrypto.KeyLen). Generate the versions with Random(corecrypto.KeyLen);
// a password stored under a keyring's
// name is refused with [KeyMaterialInvalid] rather than stretched, because
// stretching would hide that it was never random. Each version is split by
// HKDF-SHA256 into an AES-256-GCM key and an HMAC-SHA256 key, so sealing and
// signing never share a key.
//
// A box is [0x01][version, 4 bytes big-endian][crypto box]; a signature is
// [0x01][version][32-byte tag]. The version is not secret — it names a key,
// and anyone holding a box can read it.
//
// It holds no state but the store and the name, reads the store on every
// call, and is safe for concurrent use.
type Keyring struct {
	// store holds the versions.
	store coresecret.Store
	// name is the secret whose versions are the keys.
	name string
}

// NewKeyring returns the keyring over the versions of name in store. It
// refuses a nil store and a malformed name at construction; it does not
// require the secret to exist yet — a Rotator's Ensure, or any Put, creates
// it, and until then Seal and Sign report core/secret.NotFound.
func NewKeyring(store coresecret.Store, name string) (keyring *Keyring, err error) {
	//: a keyring with no store has nowhere to read a key from.
	if store == nil {
		//: InvalidConfig, naming the setting.
		return nil, wrapAs(InvalidConfig, nil, errs.String("setting", "Store"), errs.String("problem", "nil"))
	}
	//: the name every call will use is checked once, here.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return nil, nameErr
	}
	//: a keyring over the secret's versions.
	return &Keyring{store: store, name: name}, nil
}

// Seal encrypts plaintext under the NEWEST version, binding aad (which may be
// nil), and returns a box carrying that version's number. It returns
// core/secret.NotFound while the secret has no version, and
// [KeyMaterialInvalid] when the newest version is not a 32-byte key.
func (k *Keyring) Seal(ctx context.Context, plaintext, aad []byte) (box []byte, err error) {
	current, getErr := k.store.Get(ctx, k.name)
	//: no current version, or no store to ask.
	if getErr != nil {
		//: the store's own verdict.
		return nil, getErr
	}
	key, keyErr := k.subkey(current, sealLabel)
	//: the newest version is not usable as a key.
	if keyErr != nil {
		//: KeyMaterialInvalid.
		return nil, keyErr
	}
	defer key.Zeroize()
	sealed, sealErr := corecrypto.Seal(sealAlgorithm, key, plaintext, k.binding(current.Version, aad))
	//: sealing fails only on an unregistered scheme, which the imports rule out.
	if sealErr != nil {
		//: the crypto verdict, unchanged: it names no key and no plaintext.
		return nil, sealErr
	}
	//: the header names the key; the box proves the rest.
	return append(header(current.Version), sealed...), nil
}

// Open decrypts a box Seal made, under the version its header names, verifying
// aad. Every failure that concerns the box — malformed, a version no longer
// kept, tampered, other associated data — is the one [SealInvalid] verdict. A
// store that could not be READ is reported as such, because "retry" and
// "reject the box" are different responses.
func (k *Keyring) Open(ctx context.Context, box, aad []byte) (plaintext []byte, err error) {
	version, sealed, parsed := parseHeader(box)
	//: too short, or a format this build does not make.
	if !parsed {
		//: SealInvalid.
		return nil, SealInvalid
	}
	key, keyErr := k.keyFor(ctx, version, sealLabel)
	//: the store could not answer, or the box names no key it holds.
	if keyErr != nil {
		//: StoreUnavailable passes through; anything else is SealInvalid.
		return nil, verdictOr(keyErr, SealInvalid)
	}
	defer key.Zeroize()
	plaintext, openErr := corecrypto.Open(key, sealed, k.binding(version, aad))
	//: tampered, truncated, or bound to other data.
	if openErr != nil {
		//: the one verdict.
		return nil, SealInvalid
	}
	//: the plaintext, authenticated.
	return plaintext, nil
}

// Sign returns a signature over message made with the NEWEST version, carrying
// its number. It fails as Seal fails.
func (k *Keyring) Sign(ctx context.Context, message []byte) (signature []byte, err error) {
	current, getErr := k.store.Get(ctx, k.name)
	//: no current version, or no store to ask.
	if getErr != nil {
		//: the store's own verdict.
		return nil, getErr
	}
	key, keyErr := k.subkey(current, signLabel)
	//: the newest version is not usable as a key.
	if keyErr != nil {
		//: KeyMaterialInvalid.
		return nil, keyErr
	}
	defer key.Zeroize()
	tag, tagErr := corecrypto.MACTag(macAlgorithm, key, k.binding(current.Version, message))
	//: tagging fails only on an unregistered scheme, which the imports rule out.
	if tagErr != nil {
		//: the crypto verdict, unchanged.
		return nil, tagErr
	}
	//: the header names the key; the tag proves the message.
	return append(header(current.Version), tag...), nil
}

// Verify checks a signature Sign made, under the version it names, in constant
// time. Every failure that concerns the signature is the one
// [SignatureInvalid] verdict; a store that could not be read is reported as
// such.
func (k *Keyring) Verify(ctx context.Context, message, signature []byte) error {
	version, tag, parsed := parseHeader(signature)
	//: too short, or a format this build does not make.
	if !parsed {
		//: SignatureInvalid.
		return SignatureInvalid
	}
	key, keyErr := k.keyFor(ctx, version, signLabel)
	//: the store could not answer, or the signature names no key it holds.
	if keyErr != nil {
		//: StoreUnavailable passes through; anything else is SignatureInvalid.
		return verdictOr(keyErr, SignatureInvalid)
	}
	defer key.Zeroize()
	valid, verifyErr := corecrypto.MACVerify(macAlgorithm, key, k.binding(version, message), tag)
	//: a wrong tag, a wrong message, or a scheme that is not registered.
	if verifyErr != nil || !valid {
		//: the one verdict.
		return SignatureInvalid
	}
	//: authentic.
	return nil
}

// keyFor finds the KEPT version numbered version and derives its subkey.
func (k *Keyring) keyFor(ctx context.Context, version int, label string) (key corecrypto.Key, err error) {
	versions, readErr := k.store.Versions(ctx, k.name)
	//: the store could not answer, or holds nothing under the name.
	if readErr != nil {
		//: the caller decides which of the two this is.
		return corecrypto.Key{}, readErr
	}
	//: the version the header names, if it is still kept.
	for _, candidate := range versions {
		//: found: derive its subkey.
		if candidate.Version == version {
			//: the purpose-bound key, or KeyMaterialInvalid.
			return k.subkey(candidate, label)
		}
	}
	//: pruned, or never existed — the box names a key this keyring retired.
	return corecrypto.Key{}, notFound(k.name)
}

// subkey derives the purpose-bound key of one version.
func (k *Keyring) subkey(version coresecret.VersionValue, label string) (key corecrypto.Key, err error) {
	//: a version is key material only when it is exactly one crypto.Key long,
	//: and its number must fit the header — compared as uint64, so the bound
	//: compiles where int is 32 bits and a negative number is refused too.
	if version.Value.Len() != corecrypto.KeyLen || version.Version < 1 || uint64(version.Version) > math.MaxUint32 {
		//: KeyMaterialInvalid, naming the secret and the version, never bytes.
		return corecrypto.Key{}, wrapAs(KeyMaterialInvalid, nil,
			errs.String("secret", k.name), errs.Int("version", version.Version))
	}
	material := version.Value.Reveal()
	defer clear(material)
	derived, deriveErr := corecrypto.Subkey(kdfAlgorithm, material, nil, label, corecrypto.KeyLen)
	//: derivation fails only on an unregistered scheme or an impossible length.
	if deriveErr != nil {
		//: the crypto verdict, unchanged.
		return corecrypto.Key{}, deriveErr
	}
	defer clear(derived)
	//: NewKey copies, so the derived buffer can be cleared.
	return corecrypto.NewKey(derived)
}

// binding is the associated data (for a box) or the signed bytes (for a
// signature): the keyring's name, the version, then the caller's bytes.
func (k *Keyring) binding(version int, tail []byte) []byte {
	bound := make([]byte, 0, len(bindingPrefix)+len(k.name)+1+versionBytes+len(tail))
	bound = append(bound, bindingPrefix...)
	bound = append(bound, k.name...)
	bound = append(bound, 0)
	bound = binary.BigEndian.AppendUint32(bound, uint32(version))
	//: the caller's associated data or message, last and unprefixed: every
	//: field before it has a fixed width or a terminator.
	return append(bound, tail...)
}

// header is the format byte and the version, big-endian.
func header(version int) []byte {
	out := make([]byte, 0, headerLen)
	out = append(out, boxFormat)
	//: subkey refused any version wider than four bytes.
	return binary.BigEndian.AppendUint32(out, uint32(version))
}

// parseHeader splits a box or a signature into its version and the rest,
// reporting false for anything this build did not make.
func parseHeader(data []byte) (version int, rest []byte, parsed bool) {
	//: too short to carry a header, or a format byte this build does not know.
	if len(data) <= headerLen || data[0] != boxFormat {
		//: not ours.
		return 0, nil, false
	}
	version = int(binary.BigEndian.Uint32(data[1:headerLen]))
	//: version 0 is never minted — numbering starts at one — and where int is
	//: 32 bits a number past its range reads negative and is no version either.
	if version < 1 {
		//: not ours.
		return 0, nil, false
	}
	//: the version and the crypto payload after the header.
	return version, data[headerLen:], true
}

// verdictOr keeps a store's retryable verdict and replaces every other failure
// with the non-oracle one: "the store is down" and "this box is not ours" call
// for different responses, and only the first is safe to tell apart.
func verdictOr(err error, oracleSafe *errs.Error) error {
	//: a store that could not be read — the caller should retry, not reject.
	if errs.HasCode(err, coresecret.CodeStoreUnavailable) {
		//: the store's verdict, unchanged.
		return err
	}
	//: NotFound, KeyMaterialInvalid, InvalidName — all "this cannot be ours".
	return oracleSafe
}
