// Package kit — sealing at rest: members sealed where kit keeps them.
package kit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// Sealing at rest (ADR 0006 §4, step 3). What kit keeps at rest — a store's
// records on disk, a message in a queue on disk — has its personal, special
// and secret members sealed, unless they are plain, and opened when kit
// reads them: the product's code, its indexes and its workflows see plain
// values. A store or a queue kept in memory keeps nothing at rest, and seals
// nothing.
//
// A member's value is replaced by a JSON string, "sealed:v1:" and the box,
// in base64url: the SDK's subject box (pkg/v1/secret, ADR 0142 there),
// whose header names the data key that sealed it — secret.SubjectOf reads
// it — and which is bound to where the value lies: the store's node ID, the
// record's key and the member's JSON pointer. A box copied into another
// record or field does not open. The value is padded to a multiple of 16
// bytes before it is sealed, so that a box does not tell true from false.
//
// A record is sealed under its subject's data key — "s:" and the subject's
// reference —, or, without a subject, under a data key of its own — "r:"
// and the record's reference. A message is sealed under its subject's, the
// dispatcher's for a queued command without one, or under data-key itself
// — a box "sealed:v1:root:" — when it has no subject at all. Every data key
// is 32 random bytes, wrapped under kit's generated secret data-key — a
// keyring rotated every 30 days, 3 versions kept, pinned by KIT_DATA_KEY —
// and filed in kit's own store, kit.privacy/store/keys. data-key's
// rotation re-wraps the data keys, never a box, and never prunes a version
// a key still needs (secret.RotatorConfig.InUse).
//
// Destroying a data key is a cryptographic erase: every box it sealed stops
// opening, wherever it was copied — a store's former values, a dead letter,
// a backup — and kit reads such a member as its zero value, never as an
// error. kit.Erase destroys the person's key (privacy_erase.go); a
// retention, or Store.Erase, destroys it once no record of that person is
// left; a record's own key goes with the record.

// The words of sealing.
const (
	// dataKeySecret is kit's generated secret the data keys are wrapped
	// under: KIT_DATA_KEY, or kit-data-key in the environment's store.
	dataKeySecret = "data-key"
	// dataKeyBytes is a data key's length.
	dataKeyBytes int = 32
	// boxPrefix starts a member sealed under a data key; rootPrefix one
	// sealed under data-key itself.
	boxPrefix  = "sealed:v1:"
	rootPrefix = boxPrefix + "root:"
	// personKey and ownKey start the reference of a data key: a person's,
	// and a record's own.
	personKey = "s:"
	ownKey    = "r:"
	// keyCacheSize and keyCacheTTL bound the data keys a run keeps opened.
	// The TTL is how long a key another process destroyed keeps opening
	// here: a store belongs to one process, and this one destroys through
	// the cache.
	keyCacheSize int           = 4096
	keyCacheTTL  time.Duration = 10 * time.Minute
	// sealPad is the block a sealed value is padded to.
	sealPad int = 16
	// sealStripes is how many stripes order the data keys' writes and
	// destructions (sealLocks).
	sealStripes int = 64
	// aloneFor bounds how long a destruction waits for the writes sealing
	// under its key's stripe.
	aloneFor time.Duration = 10 * time.Second
	// bindDomain separates what kit binds a box under data-key to.
	bindDomain = "kit/v1 sealed at rest\x00"
)

// errErased says a sealed value's data key is gone: an erasure destroyed it,
// or — a message sealed under data-key itself — the version that sealed it
// was pruned. kit reads the value as its zero value.
var (
	errErased = errs.New(CodeSealErased, "SEAL_ERASED", "a sealed value's key is destroyed",
		"kit: a sealed value's data key is destroyed, or the data-key version that sealed it was pruned")
	// errNotCurrent ends a replacement of a key that changed meanwhile.
	errNotCurrent = errs.New(CodeSealKeyMoved, "SEAL_KEY_MOVED", "the wrapped key changed meanwhile",
		"kit: a data key's replacement found the wrapped key changed by another write")
)

// sealer seals and opens the members of an app's data at rest, in one run:
// the data keys, filed in kit's own store and wrapped under data-key, and
// data-key's keyring itself.
type sealer struct {
	keys  *secret.SubjectKeys
	root  *secret.Keyring
	refs  *referenceKeys
	locks *sealLocks
}

// sealLocks order, per data key, the writes that seal under it and its
// destruction: a write holds its key's stripe shared from its seal to its
// commit, and a destruction holds it alone while it checks that nothing
// kept still needs the key (alone).
type sealLocks [sealStripes]sync.RWMutex

// of is the stripe of the data key ref.
func (l *sealLocks) of(ref string) *sync.RWMutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(ref))
	return &l[h.Sum32()%uint32(len(l))]
}

// alone takes the stripe of the data key ref alone, for its destruction,
// and returns what releases it. It never waits as a writer the mutex knows
// of: a write holding the stripe shared may wait on another write — a
// workflow's transition its store's hook reaches — that takes it shared
// too, and a waiting writer would stop that one, and both. It tries, and
// sleeps between tries; past aloneFor, or ctx's end, it is kit's
// CodeSealShred and the key stays.
func (z *sealer) alone(ctx context.Context, ref string) (func(), error) {
	lock := z.locks.of(ref)
	deadline := time.Now().Add(aloneFor)
	wait := 100 * time.Microsecond
	for !lock.TryLock() {
		if time.Now().After(deadline) {
			return nil, failure(CodeSealShred, "SEAL_SHRED_BUSY", "a data key could not be destroyed: writes under it did not end", nil)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		wait = min(2*wait, 20*time.Millisecond)
	}
	return lock.Unlock, nil
}

// personRef is the reference of a person's data key.
func (k *referenceKeys) personRef(identity string) string {
	return personKey + k.subjectRef(identity)
}

// ownRef is the reference of a record's own data key.
func (k *referenceKeys) ownRef(store, key string) string {
	return ownKey + k.recordRef(store, key)
}

// seal seals plaintext, a member's JSON, under the data key ref — data-key
// itself when ref is "" — bound to bind, and returns the member's value: a
// JSON string.
func (z *sealer) seal(ctx context.Context, ref string, plaintext []byte, bind ...string) ([]byte, error) {
	padded := padded(plaintext)
	var box []byte
	var err error
	prefix := boxPrefix
	if ref == "" {
		prefix = rootPrefix
		box, err = z.root.Seal(ctx, padded, bindAAD(bind))
	} else {
		box, err = z.keys.Seal(ctx, ref, padded, bind...)
	}
	clear(padded)
	if err != nil {
		return nil, explain(CodeSealWrite, "SEAL_WRITE", "a member could not be sealed: kit's data keys could not be read or written", err)
	}
	out := make([]byte, 0, len(prefix)+base64.RawURLEncoding.EncodedLen(len(box))+2)
	out = base64.RawURLEncoding.AppendEncode(append(append(out, '"'), prefix...), box)
	return append(out, '"'), nil
}

// boxBytes reads a member's value, a box: whether it is one under data-key
// itself, and the SDK's box. ok is false for anything that is no box.
func boxBytes(value []byte) (root bool, box []byte, ok bool) {
	text, ok := unquote(value)
	if !ok || !bytes.HasPrefix(text, []byte(boxPrefix)) {
		return false, nil, false
	}
	b64 := text[len(boxPrefix):]
	if root = bytes.HasPrefix(text, []byte(rootPrefix)); root {
		b64 = text[len(rootPrefix):]
	}
	box = make([]byte, base64.RawURLEncoding.DecodedLen(len(b64)))
	n, err := base64.RawURLEncoding.Decode(box, b64)
	if err != nil {
		return false, nil, false
	}
	return root, box[:n], true
}

// open opens a member's value, a box, bound to bind, and returns the
// member's JSON. A box whose key is destroyed is errErased; one that is
// altered, or bound elsewhere, or whose key does not unwrap, is kit's
// CodeSealOpen.
func (z *sealer) open(ctx context.Context, value []byte, bind ...string) ([]byte, error) {
	root, raw, ok := boxBytes(value)
	if !ok {
		return nil, sealInvalid(nil)
	}
	var plain []byte
	var err error
	if root {
		plain, err = z.root.Open(ctx, raw, bindAAD(bind))
		if errors.Is(err, secret.SealInvalid) {
			// A version pruned since, or a box altered: a message outliving
			// data-key's kept versions reads as empty (ADR 0006 §4).
			return nil, errErased
		}
	} else {
		plain, err = z.keys.Open(ctx, raw, bind...)
	}
	switch {
	case err == nil:
		return unpadded(plain), nil
	case errors.Is(err, secret.KeyDestroyed):
		return nil, errErased
	case errors.Is(err, secret.SealInvalid):
		return nil, sealInvalid(err)
	case errors.Is(err, secret.SubjectKeyUnreadable):
		return nil, explain(CodeSealOpen, "SEAL_KEY_UNREADABLE",
			"a data key does not unwrap under kit's data-key: data-key was replaced, or the version that wrapped it was pruned", err)
	}
	return nil, explain(CodeSealKey, "SEAL_KEYS", "kit's data keys could not be read", err)
}

// sealInvalid is a box that does not open where it lies: altered, or moved
// from another record or field.
func sealInvalid(cause error) error {
	return explain(CodeSealOpen, "SEAL_INVALID", "a sealed member does not open: it was altered, or moved from where it was sealed", cause)
}

// boxRef is the data key a member's value names; false when it is no box
// under a data key.
func boxRef(value []byte) (string, bool) {
	root, raw, ok := boxBytes(value)
	if !ok || root {
		return "", false
	}
	ref, err := secret.SubjectOf(raw)
	if err != nil {
		return "", false
	}
	return ref, true
}

// padded is plaintext, a JSON value, padded with spaces to a multiple of
// sealPad bytes: JSON ignores them, and the box no longer tells a short
// value from another.
func padded(plaintext []byte) []byte {
	n := (len(plaintext)/sealPad + 1) * sealPad
	out := make([]byte, n)
	copy(out, plaintext)
	for i := len(plaintext); i < n; i++ {
		out[i] = ' '
	}
	return out
}

// unpadded is a sealed value without its padding.
func unpadded(plain []byte) []byte { return bytes.TrimRight(plain, " ") }

// bindAAD is what a box under data-key itself is bound to: each part behind
// its length, as the SDK binds a subject box.
func bindAAD(parts []string) []byte {
	out := []byte(bindDomain)
	for _, p := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(p)))
		out = append(out, p...)
	}
	return out
}

// destroy destroys the data key ref: every box it sealed stops opening. The
// caller holds ref's stripe alone.
func (z *sealer) destroy(ctx context.Context, ref string) (bool, error) {
	done, err := z.keys.Destroy(ctx, ref)
	if err != nil {
		return false, failure(CodeSealShred, "SEAL_SHRED", "a data key could not be destroyed", err)
	}
	return done, nil
}

// kit's own store of data keys -------------------------------------------------

// wrappedKey is one data key as kit keeps it, in kit.privacy/store/keys:
// its reference, and the key wrapped under data-key, which nothing but
// data-key opens.
type wrappedKey struct {
	Ref     string `json:"ref"`
	Wrapped []byte `json:"wrapped"`
}

// key is the data key's reference, the store's key.
func (w wrappedKey) key() string { return w.Ref }

// keyStore is the SDK's secret.SubjectKeyStore over kit's own store: an
// insertion refused over a key taken, a replacement that compares first,
// both atomic under the store's writers' lock, a deletion that removes.
type keyStore struct{ s *StoreService[wrappedKey] }

// Get reads the wrapped key ref; false when there is none.
func (k keyStore) Get(ctx context.Context, ref string) ([]byte, bool, error) {
	w, err := k.s.read(ctx, ref)
	switch {
	case isNotFound(err):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	return w.Wrapped, true, nil
}

// Insert files the wrapped key ref; false when ref is taken.
func (k keyStore) Insert(ctx context.Context, ref string, wrapped []byte) (bool, error) {
	err := k.s.write(ctx, wrappedKey{Ref: ref, Wrapped: bytes.Clone(wrapped)}, insertOnly)
	if isConflict(err) {
		return false, nil
	}
	return err == nil, err
}

// Replace replaces the wrapped key ref when it is still current; false when
// it changed meanwhile or is gone.
func (k keyStore) Replace(ctx context.Context, ref string, current, next []byte) (bool, error) {
	_, err := k.s.modify(ctx, ref, func(w *wrappedKey) error {
		if !bytes.Equal(w.Wrapped, current) {
			return errNotCurrent
		}
		w.Wrapped = bytes.Clone(next)
		return nil
	})
	if errors.Is(err, errNotCurrent) || isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes the wrapped key ref; false when there was none.
func (k keyStore) Delete(ctx context.Context, ref string) (bool, error) {
	err := k.s.remove(ctx, ref)
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// All are every wrapped key kit keeps.
func (k keyStore) All(ctx context.Context) iter.Seq2[secret.WrappedKey, error] {
	return func(yield func(secret.WrappedKey, error) bool) {
		all, err := k.s.all(ctx)
		if err != nil {
			yield(secret.WrappedKey{}, err)
			return
		}
		for _, w := range all {
			if !yield(secret.WrappedKey{Subject: w.Ref, Wrapped: w.Wrapped}, nil) {
				return
			}
		}
	}
}

// The app's side ---------------------------------------------------------------

// sealing is the app's sealer for this run, made the first time something
// seals or opens: kit's own store of data keys open, data-key found — made
// when kit keeps it and it is not there yet.
func (a *App) sealing(ctx context.Context) (*sealer, error) {
	a.privacy.mu.Lock()
	z, keys, dataKey := a.privacy.sealer, a.privacy.keyStore, a.privacy.dataKey
	a.privacy.mu.Unlock()
	if z != nil {
		return z, nil
	}
	if keys == nil || dataKey == nil {
		return nil, failure(CodeSealKey, "SEAL_NO_KEYS", "this app keeps no data keys: nothing of it is sealed", nil)
	}
	root, err := a.dataKeyring(ctx, dataKey)
	if err != nil {
		return nil, err
	}
	refs, err := a.referenceKeys(ctx)
	if err != nil {
		return nil, err
	}
	sk, err := secret.NewSubjectKeys(secret.SubjectKeysConfig{
		Root: root, Store: keyStore{s: keys},
		CacheSize: keyCacheSize, CacheTTL: keyCacheTTL, Clock: a.clock,
	})
	if err != nil {
		return nil, failure(CodeSealKey, "SEAL_KEYS", "kit's data keys could not be set up", err)
	}
	z = &sealer{keys: sk, root: root, refs: refs, locks: &a.privacy.locks}
	a.privacy.mu.Lock()
	defer a.privacy.mu.Unlock()
	if a.privacy.sealer == nil {
		a.privacy.sealer = z
	}
	return a.privacy.sealer, nil
}

// dataKeyring is data-key's keyring: the running secret's, or — for the
// command line, which runs no secret's rotation — the secret found where
// the environment keeps it, made when kit keeps it and it is not yet.
func (a *App) dataKeyring(ctx context.Context, sec *Secret) (*secret.Keyring, error) {
	if r := sec.running(); r != nil {
		return r.keyring, nil
	}
	r, err := a.secretsNow().resolve(ctx, a, sec)
	if err != nil {
		return nil, err
	}
	if sec.opts.generated && !r.pinned {
		rotator, err := secret.NewRotator(secret.RotatorConfig{
			Store: r.store, Name: r.name, Clock: a.clock,
			Policy: secret.Policy{Every: sec.opts.every, Keep: sec.opts.keep, Generate: secret.Random(sec.opts.bytes)},
		})
		if err == nil {
			_, err = rotator.Ensure(ctx)
		}
		if err != nil {
			return nil, failure(CodeSecretRotate, "SECRET_CREATE", "a generated secret could not be made", err, errs.String("secret", sec.id))
		}
	}
	keyring, err := secret.NewKeyring(r.store, r.name)
	if err != nil {
		return nil, failure(CodeSecretStore, "SECRET_KEYRING", "a secret's keyring could not be built", err, errs.String("secret", sec.id))
	}
	return keyring, nil
}

// dataKeysInUse is data-key's rotation's InUse: the oldest version a data
// key is still wrapped under, which the rotation does not prune.
func (a *App) dataKeysInUse(ctx context.Context) (int, error) {
	z, err := a.sealing(ctx)
	if err != nil {
		return 0, err
	}
	return z.keys.OldestRoot(ctx)
}

// rewrapDataKeys moves every data key to data-key's newest version: after
// a rotation, and at the start, to finish a pass a stop interrupted. It
// touches no box. What it cannot re-wrap is said on the app's log and to
// the Studio; nothing else is refused for it.
func (a *App) rewrapDataKeys(ctx context.Context) {
	z, err := a.sealing(ctx)
	if err == nil {
		var report secret.RewrapReport
		report, err = z.keys.Rewrap(ctx)
		if err == nil {
			if report.Rewrapped > 0 {
				logger.Info(ctx, a.log, "kit's data keys moved to data-key's newest version",
					logger.Int("rewrapped", report.Rewrapped), logger.Int("version", report.Root))
			}
			return
		}
		logger.Warn(ctx, a.log, "some of kit's data keys could not be re-wrapped", logger.Int("unreadable", report.Unreadable),
			logger.Int("rewrapped", report.Rewrapped), logger.String("error", errs.PublicOf(err)))
	}
	if keys := a.privacyKeyStore(); keys != nil {
		a.problem(keys.id, say("seal.rewrap", "secret", privacyService+"/secret/"+dataKeySecret, "detail", describeText(
			failure(CodeSealRewrap, "SEAL_REWRAP", "data-key's rotation could not re-wrap every data key", err))))
	}
}

// privacyKeyStore is kit's own store of data keys, nil when the app keeps
// none.
func (a *App) privacyKeyStore() *StoreService[wrappedKey] {
	a.privacy.mu.Lock()
	defer a.privacy.mu.Unlock()
	return a.privacy.keyStore
}

// sealingSource is a building block that keeps values at rest — a store, a
// topic, a queued command — and says whether it seals them.
type sealingSource interface {
	sealsAtRest(a *App) bool
}

// sealsAnything reports whether the app keeps something sealed at rest: a
// store, a topic or a queued command of the product's whose values have a
// member kit seals, kept on disk — or a data directory that already holds
// kit's data keys, whatever an earlier version of the product sealed. Only
// such an app, or one that keeps personal data, gains kit's own service.
func (a *App) sealsAnything() bool {
	if !a.sealsOnDisk() {
		return false
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, privacyService, "keys.json")); err == nil {
		return true
	}
	for _, svc := range a.services {
		if svc == nil || kitOwn(svc) {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, ok := n.(sealingSource); ok && s.sealsAtRest(a) {
				return true
			}
		}
	}
	return false
}

// sealsOnDisk reports whether a's queues and stores are on disk: what a
// message or a record sealed needs.
func (a *App) sealsOnDisk() bool { return a != nil && a.dataDir != "" && !a.opts.memory }

// measureOf is the register's measure for a store's sealing: sealed when it
// seals at rest, not sealed otherwise — in memory, or its members plain.
func measureOf(seals bool) string {
	if seals {
		return model.MeasureSealed
	}
	return model.MeasureNotSealed
}
