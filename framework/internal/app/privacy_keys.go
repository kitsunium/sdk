package app

import (
	"context"
	"encoding/hex"
	"errors"
	"maps"
	"reflect"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/crypto/kdf"
	"github.com/kitsunium/sdk/pkg/v1/crypto/mac"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// References (ADR 0006 §2): a person's records are found by the reference
// of their identity, and a record is named in the journal and the holds by
// its own — HMAC-SHA256 under subkeys of kit's generated secret index-key.

const (
	// subjectIndex is the index kit adds to a store whose entity has a
	// subject: its keys are subject references. No index a product declares
	// can have a colon in its name.
	subjectIndex = frameworkName + ":subject"
	// indexSecret is kit's generated secret the references are keyed by:
	// <PRODUCT>_INDEX_KEY, or app-index-key in the environment's store. It is
	// never rotated.
	indexSecret = "index-key"
	// indexKeyBytes is the size of the index key kit generates.
	indexKeyBytes int = 32
	// minIndexKey is the least a pinned index key may hold.
	minIndexKey int = 16
	// refBytes is how much of an HMAC a reference keeps: 128 bits.
	refBytes int = 16
)

// referenceKeys are the subkeys of index-key a run references with, and
// files index keys on a database with, derived under one set of labels
// (renamed_labels.go).
type referenceKeys struct {
	subject, record, index mac.Key
}

// derivedKeys are the reference keys a run derives from index-key: those
// every reference is made with, under presentLabels, and those a former
// release made them with, under formerLabels — read, never written, for the
// window of ADR 0180 Q12 (renamed_refs.go).
type derivedKeys struct {
	present, former *referenceKeys
}

// referenceKeys are the reference keys every reference the app makes is made
// with: the data keys, the subject index, the holds, a database's index
// rows, the journal's references (presentLabels).
func (a *App) referenceKeys(ctx context.Context) (*referenceKeys, error) {
	k, err := a.derivedKeys(ctx)
	if err != nil {
		return nil, err
	}
	return k.present, nil
}

// derivedKeys derives the subkeys of index-key once per run: <PRODUCT>_INDEX_KEY
// when the environment pins it, else app-index-key in the environment's
// store, made on first use.
func (a *App) derivedKeys(ctx context.Context) (*derivedKeys, error) {
	k, made, err := a.deriveKeys(ctx)
	if made {
		// The configuration the start read said it was set nowhere. The
		// app's lock is taken once the privacy's is released.
		a.settingFoundIn(a.variable(varIndexKeyStem), model.SettingStore)
	}
	return k, err
}

// deriveKeys is derivedKeys under the privacy's lock; made says kit made
// the index key now.
func (a *App) deriveKeys(ctx context.Context) (_ *derivedKeys, made bool, _ error) {
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
	k := &derivedKeys{}
	if k.present, err = subkeysOf(ikm, presentLabels); err == nil {
		k.former, err = subkeysOf(ikm, formerLabels)
	}
	if err != nil {
		return nil, made, failure(CodePrivacyKey, "INDEX_KEY_DERIVE", "the framework's index key could not be derived", err)
	}
	a.privacy.keys = k
	return k, made, nil
}

// subkeysOf derives the reference keys from the index key's bytes, under
// labels.
func subkeysOf(ikm []byte, labels referenceLabels) (*referenceKeys, error) {
	k := &referenceKeys{}
	for label, key := range map[string]*mac.Key{labels.subject: &k.subject, labels.record: &k.record, labels.index: &k.index} {
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
	v, _, err := a.frameworkSecret(ctx, indexSecret)
	switch {
	case err == nil:
		raw := v.Value.Reveal()
		if len(raw) < minIndexKey {
			clear(raw)
			return nil, false, failure(CodePrivacyKey, "INDEX_KEY_SHORT", "<PRODUCT>_INDEX_KEY must hold at least 16 bytes; 32 random ones are best: openssl rand -base64 32", nil)
		}
		return raw, false, nil
	case !errors.Is(err, secret.NotFound):
		return nil, false, failure(CodePrivacyKey, "INDEX_KEY_READ", "the framework's index key could not be read", err)
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
			"the index key needs a writable store — <PRODUCT>_SECRETS=file:<dir> or memory — or <PRODUCT>_INDEX_KEY to pin it", nil)
	}
	value, err := secret.Random(indexKeyBytes)()
	if err == nil {
		var kept secret.Versioned
		if kept, err = st.kept.Put(ctx, ownSecretPrefix+indexSecret, value); err == nil {
			return kept.Value.Reveal(), nil
		}
	}
	return nil, failure(CodePrivacyKey, "INDEX_KEY_MAKE", "the framework's index key could not be made", err)
}

// usesIndexKey reports whether the app makes and reads index-key: it keeps
// personal data — its subjects' references, its records' in the holds and
// the journal —, seals what it keeps on disk — its data keys are named by
// references —, or keeps an indexed store on a database, whose index keys
// the table files hashed under it (ADR 0004).
func (a *App) usesIndexKey() bool {
	if a.keepsPersonalData() || a.keepsDataKeys() {
		return true
	}
	for _, st := range a.placedStores() {
		if p := a.placementOf(st); p.db != nil && !p.memory && st.indexed() && a.storesOnDatabase(p.db) {
			return true
		}
	}
	return false
}

// indexKeyProblems warns of index-key kept in memory alone — the
// environment keeps its secrets in memory, <PRODUCT>_INDEX_KEY is unset — while
// what the app keeps persists: made again at every start, it would no
// longer find what it referenced before a restart — a hold, a record's own
// data key, a person's references — and a database would file its index
// rows again. In dev, in config and at every start.
func (a *App) indexKeyProblems() []model.Diagnostic {
	st := a.secrets.Load()
	if st == nil || st.kept == nil || st.from != model.SecretFromMemory || !a.sealsOnDisk() || !a.usesIndexKey() {
		return nil
	}
	if _, err := st.frameworkEnv.Get(context.Background(), indexSecret); !errors.Is(err, secret.NotFound) {
		return nil // pinned — or a variable the first use refuses, saying why
	}
	return []model.Diagnostic{diagnosticOf("warning", "", nil, say("privacy.index-key-memory", "variable", a.variable(varIndexKeyStem), "prefix", appPrefix(a.name)))}
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
	return hex.EncodeToString(k.indexKey(frameworkName+":fingerprint", frameworkName+"/v1")[:8])
}

// ref is the reference of text under key: a truncated HMAC that names it
// without revealing it.
func (k *referenceKeys) ref(key mac.Key, text string) string {
	tag, err := mac.Tag(mac.HMACSHA256, key, []byte(text))
	if err != nil {
		panic("app: HMAC-SHA256 is not registered: " + err.Error()) // the SDK registers it
	}
	return hex.EncodeToString(tag[:refBytes])
}

// A store's part --------------------------------------------------------------

// privacyDeclared reports whether the store declares a privacy option.
func (s *storeCore) privacyDeclared() bool { return s.privacy != nil }

// heldUntil is the instant the store's HeldUntil holds v until, when it
// declares one and v gives one.
func (s *storeCore) heldUntil(v any) (until time.Time, held bool) {
	p := s.privacy
	if p == nil || p.heldUntil == nil {
		return time.Time{}, false
	}
	defer func() {
		if recover() != nil {
			// A function that panics holds the record: kept, never lost.
			until, held = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), true
		}
	}()
	return p.heldUntil(v)
}

// heldAt says whether a hold, or the store's HeldUntil, keeps v at now.
func (s *storeCore) heldAt(ctx context.Context, a *App, key string, v any, now time.Time) (bool, error) {
	if _, held, err := a.holdOf(ctx, s.id, key); held || err != nil {
		return held, err
	}
	until, held := s.heldUntil(v)
	return held && until.After(now), nil
}

// frameworkIndexes are the indexes kit adds to the store: its subject's, when its
// entity has one. They are kept by the SDK's document store with the
// product's own, in the same write, and rebuilt at every start.
func (s *storeCore) frameworkIndexes(ctx context.Context, a *App) ([]plug.IndexSpec, error) {
	plan := s.plan()
	if plan.subject == nil || frameworkOwn(s.svc) {
		return nil, nil
	}
	keys, err := a.referenceKeys(ctx)
	if err != nil {
		return nil, err
	}
	return []plug.IndexSpec{{Name: subjectIndex, Keys: func(d plug.DocumentValue) []string {
		if d.Value == nil {
			return nil
		}
		if id, ok := plan.subjectOf(reflect.ValueOf(d.Value)); ok && id != "" {
			return []string{keys.subjectRef(id)}
		}
		return nil
	}}}, nil
}

// subjectKeys are the keys of the records whose subject is one of ids, in
// key order.
func (s *storeCore) subjectKeys(ctx context.Context, a *App, ids []string) ([]string, error) {
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
		if err == nil {
			var vs []any
			if vs, err = s.valuesOf(found); err == nil {
				for _, v := range vs {
					seen[s.keyOf(v)] = true
				}
			}
		}
		if err != nil {
			return nil, s.said(err, "", subjectIndex)
		}
	}
	delete(seen, "")
	return slices.Sorted(maps.Keys(seen)), nil
}
