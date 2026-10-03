// Package kit — the keys that index personal data without revealing it.
package kit

import (
	"context"
	"encoding/hex"
	"errors"
	"maps"
	"reflect"
	"slices"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/crypto/kdf"
	"github.com/kitsunium/sdk/pkg/v1/crypto/mac"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// References (ADR 0006 §2): a person's records are found by the reference
// of their identity, and a record is named in the journal and the holds by
// its own — HMAC-SHA256 under subkeys of kit's generated secret index-key.

const (
	// subjectIndex is the index kit adds to a store whose entity has a
	// subject: its keys are subject references. No index a product declares
	// can have a colon in its name.
	subjectIndex = "kit:subject"
	// indexSecret is kit's generated secret the references are keyed by:
	// KIT_INDEX_KEY, or kit-index-key in the environment's store. It is
	// never rotated.
	indexSecret = "index-key"
	// indexKeyBytes is the size of the index key kit generates.
	indexKeyBytes int = 32
	// minIndexKey is the least a pinned index key may hold.
	minIndexKey int = 16
	// refBytes is how much of an HMAC a reference keeps: 128 bits.
	refBytes int = 16
)

// The HKDF labels of the index key's subkeys: one per kind of reference, so
// that a subject's reference and a record's never meet — and one for the
// index keys a store on a database files (ADR 0004).
const (
	subjectLabel = "kit/v1 subject reference"
	recordLabel  = "kit/v1 record reference"
	indexLabel   = "kit/v1 database index key"
)

// referenceKeys are the subkeys of index-key a run references with, and
// files index keys on a database with.
type referenceKeys struct {
	subject, record, index mac.Key
}

// referenceKeys derives the subkeys of index-key once per run: KIT_INDEX_KEY
// when the environment pins it, else kit-index-key in the environment's
// store, made on first use.
func (a *App) referenceKeys(ctx context.Context) (*referenceKeys, error) {
	k, made, err := a.deriveKeys(ctx)
	if made {
		// The configuration the start read said it was set nowhere. The
		// app's lock is taken once the privacy's is released.
		a.settingFoundIn("KIT_INDEX_KEY", model.SettingStore)
	}
	return k, err
}

// deriveKeys is referenceKeys under the privacy's lock; made says kit made
// the index key now.
func (a *App) deriveKeys(ctx context.Context) (_ *referenceKeys, made bool, _ error) {
	a.privacy.mu.Lock()
	defer a.privacy.mu.Unlock()
	if a.privacy.keys != nil {
		return a.privacy.keys, false, nil
	}
	ikm, made, err := a.indexKey(ctx)
	if err != nil {
		return nil, false, err
	}
	defer clear(ikm)
	k, err := subkeysOf(ikm)
	if err != nil {
		return nil, made, failure(CodePrivacyKey, "INDEX_KEY_DERIVE", "kit's index key could not be derived", err)
	}
	a.privacy.keys = k
	return k, made, nil
}

// subkeysOf derives the reference keys from the index key's bytes.
func subkeysOf(ikm []byte) (*referenceKeys, error) {
	k := &referenceKeys{}
	for label, key := range map[string]*mac.Key{subjectLabel: &k.subject, recordLabel: &k.record, indexLabel: &k.index} {
		raw, err := kdf.Subkey(kdf.HKDFSHA256, ikm, nil, label, kdf.KeyLen)
		if err == nil {
			*key, err = mac.NewKey(raw)
			clear(raw)
		}
		if err != nil {
			return nil, err
		}
	}
	return k, nil
}

// indexKey reads index-key: its variable first, then the environment's
// store, where kit makes it on first use and never rotates it. made says kit
// made it now.
func (a *App) indexKey(ctx context.Context) (raw []byte, made bool, err error) {
	v, _, err := a.kitSecret(ctx, indexSecret)
	switch {
	case err == nil:
		raw := v.Value.Reveal()
		if len(raw) < minIndexKey {
			clear(raw)
			return nil, false, failure(CodePrivacyKey, "INDEX_KEY_SHORT", "KIT_INDEX_KEY must hold at least 16 bytes; 32 random ones are best: openssl rand -base64 32", nil)
		}
		return raw, false, nil
	case !errors.Is(err, secret.NotFound):
		return nil, false, failure(CodePrivacyKey, "INDEX_KEY_READ", "kit's index key could not be read", err)
	}
	raw, err = a.makeIndexKey(ctx)
	return raw, err == nil, err
}

// makeIndexKey makes index-key in the environment's store: 32 random bytes,
// kept there for good.
func (a *App) makeIndexKey(ctx context.Context) ([]byte, error) {
	st := a.secretsNow()
	if st.kept == nil {
		return nil, failure(CodePrivacyKey, "INDEX_KEY_NOT_KEPT",
			"kit's index key needs a store kit can write — KIT_SECRETS=file:<dir> or memory — or KIT_INDEX_KEY to pin it", nil)
	}
	value, err := secret.Random(indexKeyBytes)()
	if err == nil {
		var kept secret.Versioned
		if kept, err = st.kept.Put(ctx, kitSecretPrefix+indexSecret, value); err == nil {
			return kept.Value.Reveal(), nil
		}
	}
	return nil, failure(CodePrivacyKey, "INDEX_KEY_MAKE", "kit's index key could not be made", err)
}

// subjectRef is the reference of a person's identity, as the product
// stores it.
func (k *referenceKeys) subjectRef(identity string) string {
	return k.ref(k.subject, identity)
}

// recordRef is the reference of a store's record.
func (k *referenceKeys) recordRef(store, key string) string {
	return k.ref(k.record, store+"\x00"+key)
}

// indexKey is an index key as a store on a database files it: its keyed
// hash, HMAC-SHA256 under the index subkey, of the index's name and the
// key. An index is an equality lookup, which the hash keeps; the key itself
// never reaches a table.
func (k *referenceKeys) indexKey(index, key string) []byte {
	tag, err := mac.Tag(mac.HMACSHA256, k.index, []byte(index+"\x00"+key))
	if err != nil {
		return nil // unreachable: HMAC-SHA256 is always registered
	}
	return tag
}

// indexKeyID names the index subkey without saying it: what a store's index
// rows were filed with, which kit files again when it changes.
func (k *referenceKeys) indexKeyID() string {
	return hex.EncodeToString(k.indexKey("kit:fingerprint", "kit/v1")[:8])
}

// ref is the reference of text under key: a truncated HMAC that names it
// without revealing it.
func (k *referenceKeys) ref(key mac.Key, text string) string {
	tag, err := mac.Tag(mac.HMACSHA256, key, []byte(text))
	if err != nil {
		panic("kit: HMAC-SHA256 is not registered: " + err.Error()) // the SDK registers it
	}
	return hex.EncodeToString(tag[:refBytes])
}

// A store's part --------------------------------------------------------------

// plan is the classification of the store's entity type.
func (s *StoreService[T]) plan() *classPlan { return planFor[T]() }

// privacyDeclared reports whether the store declares a privacy option.
func (s *StoreService[T]) privacyDeclared() bool { return s.privacy != nil }

// subjectOf is v's subject, as written; "" when it has none.
func (s *StoreService[T]) subjectOf(v T) string {
	id, _ := s.plan().subjectOf(reflect.ValueOf(v))
	return id
}

// kitIndexes are the indexes kit adds to the store: its subject's, when its
// entity has one. They are kept by the SDK's document store with the
// product's own, in the same write, and rebuilt at every start.
func (s *StoreService[T]) kitIndexes(ctx context.Context, a *App) ([]docstore.IndexSpec[T], error) {
	plan := s.plan()
	if plan.subject == nil || kitOwn(s.svc) {
		return nil, nil
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return nil, err
	}
	return []docstore.IndexSpec[T]{docstore.Index(subjectIndex, func(v T) []string {
		if id, ok := plan.subjectOf(reflect.ValueOf(v)); ok && id != "" {
			return []string{keys.subjectRef(id)}
		}
		return nil
	})}, nil
}

// subjectKeys are the keys of the records whose subject is one of ids, in
// key order.
func (s *StoreService[T]) subjectKeys(ctx context.Context, a *App, ids []string) ([]string, error) {
	eng := s.engine()
	if eng == nil {
		return nil, notRunning(&s.nodeBase)
	}
	if s.plan().subject == nil {
		return nil, nil
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		found, err := eng.Find(ctx, subjectIndex, keys.subjectRef(id))
		if err != nil {
			return nil, s.said(err, "", subjectIndex)
		}
		for _, v := range found {
			seen[s.keyOf(v)] = true
		}
	}
	delete(seen, "")
	return slices.Sorted(maps.Keys(seen)), nil
}
