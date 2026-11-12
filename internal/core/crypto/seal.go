package crypto

// Version is the box wire-format version, the first byte of every sealed box
// ([Version][id][nonce][ciphertext||tag]). It is frozen post-v1.0.0; a future
// framing change bumps it and Open rejects an unknown version as
// DecryptionFailed. Concrete AEADs embed it via this constant.
const Version byte = 0x01

// headerLen is the fixed prefix every box carries before its scheme-specific
// nonce: one Version byte + one algorithm-id byte.
const headerLen int = 2

// Seal encrypts plaintext under the AEAD registered as name, binding aad, and
// returns the self-framed box. A name with no registered scheme returns
// UnknownAlgorithm (blank-import the scheme's package to register it).
func Seal(name Algorithm, key Key, plaintext, aad []byte) (box []byte, err error) {
	//: resolve the scheme first so a missing import surfaces a clear sentinel.
	scheme, ok := Lookup(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownAlgorithm
	}
	//: delegate to the resolved scheme, which owns its full wire framing.
	return scheme.Seal(key, plaintext, aad)
}

// Open decrypts a box produced by Seal, verifying aad, and returns the
// plaintext. The scheme is resolved from the box's 1-byte id header — the
// caller never names it. Any failure (short box, unknown id, bad tag, wrong
// key) returns the single non-oracle DecryptionFailed.
func Open(key Key, box, aad []byte) (plaintext []byte, err error) {
	//: a box must carry at least the Version + id header to be dispatchable.
	if len(box) < headerLen {
		//: too short to name a scheme — non-oracle failure.
		return nil, DecryptionFailed
	}
	//: byte 1 is the algorithm id; resolve the scheme without trusting it yet.
	scheme, ok := lookupByID(box[1])
	//: an unknown id is indistinguishable from tampering — same non-oracle error.
	if !ok {
		//: never reveal that the id was unknown vs the tag was bad.
		return nil, DecryptionFailed
	}
	//: the scheme re-validates the full framing (incl. Version) and decrypts.
	return scheme.Open(key, box, aad)
}

// NewSealer binds key to the AEAD registered as name and returns a [Sealer]
// that seals under it many times, with the cipher built once (#813). A scheme
// that implements [Binder] builds it in Bind; any other is wrapped so each
// SealTo still calls its Seal. A name with no registered scheme returns
// UnknownAlgorithm; a key the scheme refuses, its error.
//
// The Sealer keeps what the scheme derived from key — for AES-GCM its round
// keys — for as long as it is referenced: Key.Zeroize does not reach it, so a
// caller that zeroizes the key drops the Sealer with it.
//
// IFACE-PLUGIN: the scheme's own Sealer comes back behind the port, its
// concrete type unexported in the scheme's package.
func NewSealer(name Algorithm, key Key) (sealer Sealer, err error) {
	//: resolve the scheme first so a missing import surfaces a clear sentinel.
	scheme, ok := Lookup(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownAlgorithm
	}
	//: a scheme that builds its cipher once binds the key itself.
	if binder, binds := scheme.(Binder); binds {
		//: the scheme's own bound cipher, or its key refusal.
		return binder.Bind(key)
	}
	//: any other scheme seals through its Seal, one box at a time.
	return sealEach{scheme: scheme, key: key}, nil
}

// sealEach is the Sealer of a scheme that does not implement [Binder]: each
// SealTo is the scheme's Seal, its box appended to dst.
type sealEach struct {
	// scheme is the registered AEAD.
	scheme AEAD
	// key is the key every box is sealed under.
	key Key
}

// SealTo seals plaintext with the scheme's Seal and appends the box to dst.
func (s sealEach) SealTo(dst, plaintext, aad []byte) (box []byte, err error) {
	sealed, sealErr := s.scheme.Seal(s.key, plaintext, aad)
	//: the scheme's own failure, dst unextended.
	if sealErr != nil {
		//: as Seal returned it.
		return dst, sealErr
	}
	//: the box after whatever dst held.
	return append(dst, sealed...), nil
}
