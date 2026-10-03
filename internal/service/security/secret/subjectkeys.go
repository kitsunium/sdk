// Package secret — subject keys: one data key per subject, wrapped by a root
// keyring that rotates, and destroyed to erase everything it sealed (ADR 0142).
package secret

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// createAttempts bounds how many times a Seal re-reads a subject whose key a
// concurrent writer created — or created and destroyed — under it. Two reads
// settle every ordinary race; a third failure means keys are being created and
// destroyed in a loop, and the store is reported rather than raced forever.
const createAttempts int = 3

// The HKDF labels that turn one data key into its AEAD key and its
// identifier. Each is followed by NUL and the subject, so the two derived
// values are independent of each other and bound to the subject.
const (
	// subjectSealLabel derives the AEAD key a subject's boxes are sealed with.
	subjectSealLabel string = "kitsunium/secret subject seal v1\x00"
	// subjectIDLabel derives the identifier every such box carries.
	subjectIDLabel string = "kitsunium/secret subject id v1\x00"
)

// SubjectKeysConfig parameterises [NewSubjectKeys]: the root that wraps every
// data key, the store that keeps them, and how many opened keys this process
// may keep, for how long.
type SubjectKeysConfig struct {
	// Root wraps every data key: a Keyring over the root secret. Its newest
	// version wraps each new key and every kept version unwraps, so a
	// rotation breaks nothing; [SubjectKeys.Rewrap] moves every key to the
	// newest version, and RotatorConfig.InUse keeps a version from being
	// pruned while a key is still wrapped under it. Required.
	Root *Keyring
	// Store keeps the wrapped keys, one per subject. Required.
	Store coresecret.SubjectKeyStore
	// CacheSize is how many opened keys this process keeps, the least
	// recently used evicted first. Zero keeps none: every Seal and Open
	// reads the store and unwraps. Negative is refused.
	CacheSize int
	// CacheTTL is how long an opened key stays cached. It is required with a
	// cache and refused without one, because it is a promise about erasure: a
	// key another PROCESS destroyed keeps opening — and sealing — here for up
	// to CacheTTL. A key destroyed through this value leaves its cache at once.
	CacheTTL time.Duration
	// Clock times the cache. nil means clock.System.
	Clock clock.Clock
}

// SubjectKeys keeps one data key per subject — a person, a tenant, a record,
// whatever the caller files keys under — and seals and opens under it:
//
//   - a data key is 32 bytes from crypto/rand, made by the first Seal for its
//     subject, wrapped under the root keyring's newest version, bound to its
//     subject, and filed in the store;
//   - a box names its subject and the identifier of the key that sealed it,
//     and is bound to whatever the caller passes as binding — where the value
//     lies — so a box moved elsewhere does not open;
//   - [SubjectKeys.Destroy] deletes a subject's key: every box sealed under
//     it, wherever it was copied, stops opening. NIST SP 800-88r2 calls this a
//     cryptographic erase;
//   - a rotation of the root re-wraps one small key per subject
//     ([SubjectKeys.Rewrap]) and never touches a box.
//
// It is safe for concurrent use.
type SubjectKeys struct {
	// root wraps and unwraps every data key.
	root *Keyring
	// store keeps them.
	store coresecret.SubjectKeyStore
	// cache keeps opened keys for a bounded time.
	cache *keyCache
}

// NewSubjectKeys validates cfg and returns the engine. It refuses a nil root,
// a nil store, a negative cache size, a cache without a CacheTTL and a
// CacheTTL without a cache, each with [InvalidConfig] naming the setting, and
// touches nothing: a key is made by the first Seal of its subject.
func NewSubjectKeys(cfg SubjectKeysConfig) (keys *SubjectKeys, err error) {
	//: every refusal is decided before anything is built.
	if invalid := cfg.validate(); invalid != nil {
		//: InvalidConfig.
		return nil, invalid
	}
	clk := cfg.Clock
	//: nil is the production default.
	if clk == nil {
		//: the system time source.
		clk = clock.System
	}
	//: an engine that has not read the store yet.
	return &SubjectKeys{
		root:  cfg.Root,
		store: cfg.Store,
		cache: newKeyCache(cfg.CacheSize, cfg.CacheTTL, clk),
	}, nil
}

// validate refuses a configuration no engine could honour.
func (c SubjectKeysConfig) validate() error {
	//: nothing to wrap keys with.
	if c.Root == nil {
		//: InvalidConfig.
		return refuseSetting("Root", "nil")
	}
	//: nowhere to keep them.
	if c.Store == nil {
		//: InvalidConfig.
		return refuseSetting("Store", "nil")
	}
	//: a negative bound means nothing.
	if c.CacheSize < 0 {
		//: InvalidConfig.
		return refuseSetting("CacheSize", "must not be negative")
	}
	//: a cache whose keys never expire would keep a destroyed key open forever.
	if c.CacheSize > 0 && c.CacheTTL <= 0 {
		//: InvalidConfig: the bound is the caller's promise, never the SDK's.
		return refuseSetting("CacheTTL", "must be positive with a cache")
	}
	//: a TTL with no cache is a cache the caller believes it has.
	if c.CacheSize == 0 && c.CacheTTL != 0 {
		//: InvalidConfig.
		return refuseSetting("CacheTTL", "set without a CacheSize")
	}
	//: an engine that can work.
	return nil
}

// Seal encrypts plaintext under subject's data key — made, wrapped and filed
// by this call when subject has none — and returns a box naming subject and
// that key. The box is bound to bind, part by part and in order: Open must be
// given the same parts, so a caller binds the box to where the value lies (a
// store, a record's key, a field's pointer) and a box moved elsewhere does not
// open. Each part is length-prefixed, so ("ab", "c") and ("a", "bc") differ.
//
// It refuses a malformed subject with core/security/secret.InvalidSubject, returns
// core/security/secret.NotFound while the root has no version, and
// [SubjectKeyUnreadable] when subject's key is held and does not unwrap — it
// never replaces such a key.
func (s *SubjectKeys) Seal(ctx context.Context, subject string, plaintext []byte, bind ...string) (box []byte, err error) {
	//: the subject is checked before the store is asked anything.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return nil, subjectErr
	}
	key, id, _, acquireErr := s.acquire(ctx, subject, true)
	//: no key could be read or made.
	if acquireErr != nil {
		//: the verdict of the store, the root, or the unwrap.
		return nil, acquireErr
	}
	defer key.Zeroize()
	header := subjectHeader(subject, id)
	sealed, sealErr := corecrypto.Seal(sealAlgorithm, key, plaintext, subjectAAD(header, bind))
	//: sealing fails only on an unregistered scheme, which the imports rule out.
	if sealErr != nil {
		//: the crypto verdict, unchanged: it names no key and no plaintext.
		return nil, sealErr
	}
	//: the header names the key; the AEAD box proves the rest.
	return append(header, sealed...), nil
}

// Open decrypts a box [SubjectKeys.Seal] made, under the key of the subject it
// names, verifying bind. It answers:
//
//   - [KeyDestroyed] when that key is not held — destroyed by an erasure, or
//     replaced by a newer key of the same subject after one: the value is
//     erased, and a caller reading records reads it as such;
//   - [SealInvalid] when the box is malformed, altered, or bound to other
//     parts;
//   - [SubjectKeyUnreadable] when the key is held and does not unwrap, which
//     is a fault and never an erasure;
//   - core/security/secret.StoreUnavailable when the store failed.
func (s *SubjectKeys) Open(ctx context.Context, box []byte, bind ...string) (plaintext []byte, err error) {
	parsed, ok := parseSubjectBox(box)
	//: not a box this engine makes.
	if !ok {
		//: SealInvalid.
		return nil, SealInvalid
	}
	key, id, cached, acquireErr := s.acquire(ctx, parsed.subject, false)
	//: destroyed, unreadable, or a store that failed.
	if acquireErr != nil {
		//: the verdict as it is.
		return nil, acquireErr
	}
	//: a cached key another process replaced: read the store once, not the cache.
	if !sameID(id, parsed.id) && cached {
		key.Zeroize()
		s.cache.forget(parsed.subject)
		key, id, _, acquireErr = s.acquire(ctx, parsed.subject, false)
		//: the key went away meanwhile.
		if acquireErr != nil {
			//: KeyDestroyed, most likely.
			return nil, acquireErr
		}
	}
	defer key.Zeroize()
	//: the key that sealed this box is not the one held: it was destroyed.
	if !sameID(id, parsed.id) {
		//: KeyDestroyed, naming the subject.
		return nil, keyDestroyed(parsed.subject)
	}
	plaintext, openErr := corecrypto.Open(key, parsed.sealed, subjectAAD(parsed.header, bind))
	//: altered, or bound to other parts.
	if openErr != nil {
		//: the one verdict for every box-shaped failure.
		return nil, SealInvalid
	}
	//: the plaintext, authenticated.
	return plaintext, nil
}

// Destroy deletes subject's data key, and reports whether one was held. Every
// box sealed under it stops opening — the copies in other records, in former
// versions, in a dead letter or a backup included — because nothing holds the
// key that would open them: the cryptographic erase of NIST SP 800-88r2.
//
// This process's cache drops and wipes the key at once. Another process that
// cached it keeps it for up to its CacheTTL. A wrapped copy of the key that
// the store's backend kept elsewhere — a write-ahead log, a backup — opens only
// while the root version that wrapped it is kept: the erasure is complete
// everywhere once that version is pruned.
//
// Destroying a subject that holds no key is not an error. A later Seal for the
// same subject makes a NEW key, which opens nothing the destroyed one sealed.
func (s *SubjectKeys) Destroy(ctx context.Context, subject string) (destroyed bool, err error) {
	//: the subject is checked before the store is asked anything.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return false, subjectErr
	}
	deleted, deleteErr := s.store.Delete(ctx, subject)
	//: after the store, never before: a fill that read the key first is shut out.
	s.cache.forget(subject)
	//: the store failed; whether the key is gone is unknown.
	if deleteErr != nil {
		//: StoreUnavailable, carrying the store's own error.
		return false, storeFailure(deleteErr, "delete", subject)
	}
	//: whether there was a key to destroy.
	return deleted, nil
}

// acquire returns a copy of subject's AEAD key, which the caller zeroizes, the
// key's identifier, and whether it came from the cache. It reads the store on
// a miss, creating the key when create is set and subject holds none.
func (s *SubjectKeys) acquire(ctx context.Context, subject string, create bool) (key corecrypto.Key, id [keyIDLen]byte, cached bool, err error) {
	//: a live cached key: no store read, no unwrap.
	if hit, found := s.cache.fetch(subject); found {
		key, id, live := hit.take()
		//: wiped between the lookup and the copy: read the store instead.
		if live {
			//: the cached key.
			return key, id, true, nil
		}
	}
	epoch := s.cache.current()
	opened, loadErr := s.load(ctx, subject, create)
	//: nothing to cache.
	if loadErr != nil {
		//: the verdict.
		return corecrypto.Key{}, id, false, loadErr
	}
	key, id, live := opened.take()
	//: the copy is taken first: offer may wipe what it does not keep.
	s.cache.offer(subject, epoch, opened)
	//: nobody else holds a key just opened, so only a malformed one is not live.
	if !live {
		//: SubjectKeyUnreadable.
		return corecrypto.Key{}, id, false, keyUnreadable(subject)
	}
	//: the key, read from the store.
	return key, id, false, nil
}

// load reads subject's key from the store and opens it, creating it first
// when create is set and none is filed.
func (s *SubjectKeys) load(ctx context.Context, subject string, create bool) (opened *openedKey, err error) {
	//: a created key another writer beat us to is re-read, a bounded number of times.
	for range createAttempts {
		wrapped, found, getErr := s.store.Get(ctx, subject)
		//: the store failed.
		if getErr != nil {
			//: StoreUnavailable.
			return nil, storeFailure(getErr, "get", subject)
		}
		//: held: unwrap it.
		if found {
			//: the opened key, or SubjectKeyUnreadable.
			return s.unwrap(ctx, subject, wrapped)
		}
		//: Open never makes a key: an absent one was destroyed.
		if !create {
			//: KeyDestroyed.
			return nil, keyDestroyed(subject)
		}
		made, inserted, createErr := s.create(ctx, subject)
		//: the root or the store failed.
		if createErr != nil {
			//: the verdict.
			return nil, createErr
		}
		//: ours was filed.
		if inserted {
			//: the new key.
			return made, nil
		}
		//: another writer filed one first: read it on the next turn.
	}
	//: keys are being created and destroyed under this subject in a loop.
	return nil, errs.Wrap(coresecret.StoreUnavailable, errs.WrapParams{},
		errs.String("operation", "insert"), errs.String("subject", subject),
		errs.String("problem", "the subject's key was created and destroyed concurrently"))
}

// create makes a data key for subject, wraps it under the root's newest
// version, and files it if no key is filed yet. It reports inserted == false,
// with no key, when another writer filed one first.
func (s *SubjectKeys) create(ctx context.Context, subject string) (opened *openedKey, inserted bool, err error) {
	dek := make([]byte, corecrypto.KeyLen)
	defer clear(dek)
	//: crypto/rand fills the buffer or ends the program; its error is kept anyway.
	if _, readErr := rand.Read(dek); readErr != nil {
		//: GenerateFailed, with the entropy source's message.
		return nil, false, wrapAs(GenerateFailed, readErr, errs.String("subject", subject))
	}
	wrapped, wrapErr := s.root.sealAs(ctx, wrapLabel, dek, wrapAAD(subject))
	//: no root version yet, one that is not a key, or a root store that failed.
	if wrapErr != nil {
		//: the root's verdict.
		return nil, false, wrapErr
	}
	filed, insertErr := s.store.Insert(ctx, subject, wrapped)
	//: the store failed.
	if insertErr != nil {
		//: StoreUnavailable.
		return nil, false, storeFailure(insertErr, "insert", subject)
	}
	//: another writer's key is the subject's key; this one was never filed.
	if !filed {
		//: nothing made.
		return nil, false, nil
	}
	settled, settleErr := s.settle(ctx, subject, dek, wrapped)
	//: the key is filed, but whether its root version survives is unknown.
	if settleErr != nil {
		//: the root's or the store's verdict; the next call reads the key.
		return nil, false, settleErr
	}
	//: destroyed, or re-wrapped by another writer, between the insert and now.
	if !settled {
		//: read it again on the next turn.
		return nil, false, nil
	}
	opened, err = deriveOpened(subject, dek)
	//: the key filed, and opened.
	return opened, err == nil, err
}

// settle moves a key just filed to the root's newest version when the root
// rotated while the key was being made, and reports whether the key filed is
// still this one.
//
// A rotation's InUse scan sees every key FILED when it runs. A key made
// between the wrap and the insert is not filed yet, so a rotation that scanned
// in that window, after another that retired nothing, may prune the version
// the key was wrapped under — and the insert would file a key nothing opens.
// Every rotation stores its new version before it scans, so reading the newest
// version once, after the insert, sees every rotation that could have missed
// the key; one that stores its version later scans later, and sees the key.
// The data key is still in hand, so a stale wrap is replaced, never lost.
func (s *SubjectKeys) settle(ctx context.Context, subject string, dek, wrapped []byte) (settled bool, err error) {
	version, _, _ := parseHeader(wrapped)
	newest, getErr := s.root.store.Get(ctx, s.root.name)
	//: the root could not be read: the key stays filed as it is.
	if getErr != nil {
		//: the root store's verdict.
		return false, getErr
	}
	//: no rotation since the wrap: the version is the newest, and kept.
	if newest.Version == version {
		//: filed as made.
		return true, nil
	}
	rewrapped, wrapErr := s.root.sealAs(ctx, wrapLabel, dek, wrapAAD(subject))
	//: the newest version is not usable, or the root store failed.
	if wrapErr != nil {
		//: the root's verdict.
		return false, wrapErr
	}
	replaced, replaceErr := s.store.Replace(ctx, subject, wrapped, rewrapped)
	//: the store failed.
	if replaceErr != nil {
		//: StoreUnavailable.
		return false, storeFailure(replaceErr, "replace", subject)
	}
	//: false when the key was destroyed or re-wrapped meanwhile.
	return replaced, nil
}

// unwrap opens a wrapped key under the root keyring.
func (s *SubjectKeys) unwrap(ctx context.Context, subject string, wrapped []byte) (opened *openedKey, err error) {
	dek, openErr := s.root.openAs(ctx, wrapLabel, wrapped, wrapAAD(subject))
	//: the root could not be read, or the key does not open under it.
	if openErr != nil {
		//: a root store that failed is worth a retry.
		if errs.HasCode(openErr, coresecret.CodeStoreUnavailable) {
			//: StoreUnavailable, unchanged.
			return nil, openErr
		}
		//: SubjectKeyUnreadable: a fault, never an erasure.
		return nil, keyUnreadable(subject)
	}
	defer clear(dek)
	//: a wrapped value that is not one key was not wrapped by this engine.
	if len(dek) != corecrypto.KeyLen {
		//: SubjectKeyUnreadable.
		return nil, keyUnreadable(subject)
	}
	//: the AEAD key and the identifier.
	return deriveOpened(subject, dek)
}

// deriveOpened derives from a data key the AEAD key its boxes are sealed with
// and the identifier they carry, by HKDF-SHA256 under two labels, both bound
// to the subject. The data key is never used as a cipher key itself.
func deriveOpened(subject string, dek []byte) (opened *openedKey, err error) {
	seal, sealErr := corecrypto.Subkey(kdfAlgorithm, dek, nil, subjectSealLabel+subject, corecrypto.KeyLen)
	//: derivation fails only on an unregistered scheme or an impossible length.
	if sealErr != nil {
		//: the crypto verdict, unchanged.
		return nil, sealErr
	}
	//: a full-length output, truncated: HKDF's expansion is prefix-consistent.
	id, idErr := corecrypto.Subkey(kdfAlgorithm, dek, nil, subjectIDLabel+subject, corecrypto.KeyLen)
	//: as above.
	if idErr != nil {
		clear(seal)
		//: the crypto verdict, unchanged.
		return nil, idErr
	}
	defer clear(id)
	opened = &openedKey{seal: seal}
	copy(opened.id[:], id)
	//: the opened key; the data key is the caller's to clear.
	return opened, nil
}

// sameID compares two key identifiers in constant time. An identifier is not
// secret, but a comparison that leaks the length of a shared prefix has no
// reason to exist in a package that compares keys.
func sameID(held, named [keyIDLen]byte) bool {
	//: one when equal.
	return subtle.ConstantTimeCompare(held[:], named[:]) == 1
}

// keyDestroyed is the KeyDestroyed verdict for one subject.
func keyDestroyed(subject string) error {
	//: the subject is a reference, and what an operator needs to act.
	return errs.Wrap(KeyDestroyed, errs.WrapParams{}, errs.String("subject", subject))
}

// keyUnreadable is the SubjectKeyUnreadable verdict for one subject.
func keyUnreadable(subject string) error {
	//: the subject is a reference, and what an operator needs to act.
	return errs.Wrap(SubjectKeyUnreadable, errs.WrapParams{}, errs.String("subject", subject))
}
