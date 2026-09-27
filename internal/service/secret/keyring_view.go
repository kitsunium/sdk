// Package secret — a keyring read once: the root view a pass over many
// wrapped keys uses.
package secret

import (
	"context"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// rootView is ONE reading of a keyring's versions, with every usable
// version's key for one purpose derived once. Seal and Open read the store and
// derive a key on every call, which is right for a call and wasteful for a
// pass over a million wrapped keys: a re-wrap reads the root once and wraps
// every key under the newest version it saw.
//
// A version that is not usable as a key — not one crypto.Key long — is left
// out: what it wrapped does not open, and the pass counts it. The newest
// version must be usable, since everything the view seals goes under it.
//
// A view holds key material, so its owner closes it, and it is not safe for
// concurrent use: one pass, one goroutine.
type rootView struct {
	// keyring supplies the binding every box is sealed with.
	keyring *Keyring
	// newest is the version the view seals under.
	newest int
	// keys maps a usable version to its key for the view's purpose.
	keys map[int]corecrypto.Key
}

// view reads the keyring's versions once and derives their keys for label —
// wrapLabel, for a re-wrap. It returns core/secret.NotFound while the root has
// no version, the store's verdict when it cannot be read, and
// [KeyMaterialInvalid] when the newest version cannot seal.
func (k *Keyring) view(ctx context.Context, label string) (view *rootView, err error) {
	versions, readErr := k.store.Versions(ctx, k.name)
	//: no root yet, or no store to ask.
	if readErr != nil {
		//: the store's own verdict.
		return nil, readErr
	}
	view = &rootView{keyring: k, newest: versions[0].Version, keys: make(map[int]corecrypto.Key, len(versions))}
	//: every kept version, newest first.
	for _, version := range versions {
		key, keyErr := k.subkey(version, label)
		//: a version that is not key material seals nothing and opens nothing.
		if keyErr != nil {
			//: the newest must seal: a view that cannot is refused whole.
			if version.Version == view.newest {
				view.close()
				//: KeyMaterialInvalid, naming the secret and the version.
				return nil, keyErr
			}
			//: an older one is skipped; what it wrapped counts as unreadable.
			continue
		}
		view.keys[version.Version] = key
	}
	//: the root, read once.
	return view, nil
}

// seal seals plaintext under the newest version, exactly as sealAs would
// have for the view's purpose.
func (v *rootView) seal(plaintext, aad []byte) (box []byte, err error) {
	//: the newest version's key was derived by view, or view refused.
	return v.keyring.sealUnder(v.keys[v.newest], v.newest, plaintext, aad)
}

// open opens a box any usable kept version sealed, exactly as openAs would
// have for the view's purpose. Every failure is the one [SealInvalid]: the
// view has read the store already, so there is no "retry" to tell apart.
func (v *rootView) open(box, aad []byte) (plaintext []byte, err error) {
	version, sealed, parsed := parseHeader(box)
	//: not a keyring box.
	if !parsed {
		//: SealInvalid.
		return nil, SealInvalid
	}
	key, kept := v.keys[version]
	//: a version pruned, never made, or not usable as a key.
	if !kept {
		//: SealInvalid.
		return nil, SealInvalid
	}
	plaintext, openErr := corecrypto.Open(key, sealed, v.keyring.binding(version, aad))
	//: tampered, or bound to other data.
	if openErr != nil {
		//: the one verdict.
		return nil, SealInvalid
	}
	//: the plaintext, authenticated.
	return plaintext, nil
}

// close zeroizes every key the view derived. The view is unusable after.
func (v *rootView) close() {
	//: each derived key, cleared in place.
	for version, key := range v.keys {
		key.Zeroize()
		delete(v.keys, version)
	}
}
