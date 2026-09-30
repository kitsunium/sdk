// Package kit — hashing and verifying passwords under their policy.
package kit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
	"github.com/kitsunium/sdk/pkg/v1/password"
)

// setAttempts bounds how often Set reads again a hash another write changed
// while it checked the password.
const setAttempts int = 3

// policyLabel labels the edges of a password policy's calls, drawn by the
// runtime and by the analyzer alike: the diagram says which nodes set,
// change or check passwords, and kit finds the endpoints to warn of
// (passwords_warn.go).
const policyLabel = "password"

// What a password policy does (ADR 0007 §2): Set for a reset flow, Change
// behind the current password, Verify for a login. Verifying costs 144 ms
// by design (the SDK's password/BENCH.md), right or wrong: a policy that
// refuses n former passwords verifies up to n+1 hashes per change, all of
// them whichever matches, concurrently, at most GOMAXPROCS at once. Whoever
// may call Change or Set learns whether a candidate was one of the last n
// passwords: Change asks for the current one, the refusal never says which
// one matched, and Set belongs behind a reset token. In dev, kit warns of an
// endpoint that reaches them with no rate limit (passwords_warn.go).

var (
	// verifyPassword is the SDK's: the tests count its calls.
	verifyPassword = password.Verify
	// hashPassword is the SDK's hashing with kit's algorithm: the tests
	// count its calls.
	hashPassword func(pw []byte) (string, error) = func(pw []byte) (string, error) { return password.Hash(password.PBKDF2SHA256, pw) }
	// isCommon is the SDK's list of the most common passwords (NotCommon).
	isCommon = password.IsCommon

	// errPasswordMoved ends a write whose hash another write changed since it
	// was read.
	errPasswordMoved = errs.New(CodePasswordMoved, "PASSWORD_MOVED", "the password changed meanwhile", "kit: a password write found the hash changed by another write")

	// needsRehash is the SDK's NeedsRehash: the tests replace the hashes.
	needsRehash = password.NeedsRehash

	// dummy is what a missing record is verified against, so that it costs
	// what a wrong password costs; the tests make a new one when they replace
	// the hashing.
	dummy = &dummyHashes{}
)

// dummyHashes is a hash of nothing anyone knows, made once per process.
type dummyHashes struct {
	once sync.Once
	hash string
}

// Set sets the password of the record under key — a reset flow, behind a
// reset token: it checks the policy — the length, the most common passwords,
// and with NotReused the former passwords —, hashes the password with the
// SDK's password.Hash and
// stores the hash; the hash it replaces goes to the field's history. A
// password the policy refuses is an [Invalid] error, which says it was used
// recently, never which one; a missing record is a [NotFound].
func (p *PasswordPolicyService[T]) Set(ctx context.Context, key string, password []byte) error {
	return p.run(ctx, model.EdgeWrites, "Set", func(ctx context.Context) error {
		return p.set(ctx, key, password, nil)
	})
}

// Change changes the password of the record under key: it verifies the
// current password first, as Symfony's UserPassword does — a wrong one is
// an [Invalid] error —, then sets next as Set does.
func (p *PasswordPolicyService[T]) Change(ctx context.Context, key string, current, next []byte) error {
	return p.run(ctx, model.EdgeWrites, "Change", func(ctx context.Context) error {
		hash, ok, err := p.verified(ctx, key, current)
		switch {
		case err != nil:
			return err
		case !ok:
			return Invalid("the current password is wrong")
		}
		return p.set(ctx, key, next, &hash)
	})
}

// Verify reports whether password is the one of the record under key, for
// a login, with the SDK's constant-time Verify. When the stored hash is
// weaker than the SDK's policy now (password.NeedsRehash), kit stores it
// again, hashed anew: the same password, so no former value. A key with no
// record — or a record with no password yet — costs what a wrong password
// costs, and answers false.
func (p *PasswordPolicyService[T]) Verify(ctx context.Context, key string, password []byte) (bool, error) {
	var ok bool
	err := p.run(ctx, model.EdgeReads, "Verify", func(ctx context.Context) error {
		hash, matched, err := p.verified(ctx, key, password)
		ok = matched
		if matched && err == nil && needsRehash(hash) {
			p.rehash(ctx, key, password, hash)
		}
		return err
	})
	return ok, err
}

// run runs one of the policy's calls in a span on its store's node.
func (p *PasswordPolicyService[T]) run(ctx context.Context, edge model.EdgeKind, name string, fn func(context.Context) error) error {
	a := p.s.app()
	if a == nil || p.member == nil {
		return notRunning(&p.s.nodeBase)
	}
	op := model.OpWrite
	if edge == model.EdgeReads {
		op = model.OpRead
	}
	ctx, sp := a.begin(ctx, &spanStart{node: p.s.id, from: currentNode(ctx), edge: edge, label: policyLabel, op: op, name: name})
	err := fn(ctx)
	sp.end(err)
	return err
}

// verified reads the record's hash and verifies password against it. A
// record that is not there, or that has no password yet, costs what a
// wrong password costs — a verification against a hash of nothing — and
// answers false; so does a hash the SDK cannot read, which is an error.
// The hash of nothing is made before anything is read, so that even the
// process's first call costs the same whatever the key.
func (p *PasswordPolicyService[T]) verified(ctx context.Context, key string, password []byte) (string, bool, error) {
	nothing := dummyHash()
	v, err := p.s.read(ctx, key)
	if err != nil && !isNotFound(err) {
		return "", false, err
	}
	hash := ""
	if err == nil {
		hash, _ = p.hashOf(&v)
	}
	if hash == "" {
		// A missing record costs what a wrong password costs; a dummy that
		// cannot be verified is a hashing that no longer works.
		if _, err := verifyPassword(password, nothing); err != nil {
			return "", false, failure(CodePasswordHash, "PASSWORD_UNREADABLE", "the password hashing does not work", err, errs.String("store", p.s.id))
		}
		return "", false, nil
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		_, dummyErr := verifyPassword(password, nothing)
		return hash, false, failure(CodePasswordHash, "PASSWORD_UNREADABLE", "the stored password hash cannot be read", errors.Join(err, dummyErr), errs.String("store", p.s.id))
	}
	return hash, ok, nil
}

// set checks, hashes and stores a password. verified is the hash Change
// verified the current password against: when another write changed it
// since, the change is a [Conflict]. Set reads a moved hash again.
func (p *PasswordPolicyService[T]) set(ctx context.Context, key string, password []byte, verified *string) error {
	if utf8.RuneCount(password) < p.minLength {
		return Invalid(fmt.Sprintf("a password has at least %d characters", p.minLength))
	}
	if p.notCommon && isCommon(password) {
		return Invalid("this password is among the most common ones: choose another one")
	}
	for range setAttempts {
		err := p.attempt(ctx, key, password, verified)
		if !errors.Is(err, errPasswordMoved) {
			return err
		}
		if verified != nil {
			break
		}
	}
	return Conflict("the password changed meanwhile: try again")
}

// attempt is one try of set: the hash read, the password checked against it
// and its former ones, then stored if the hash is still the one read.
func (p *PasswordPolicyService[T]) attempt(ctx context.Context, key string, password []byte, verified *string) error {
	v, err := p.s.read(ctx, key)
	if err != nil {
		return err
	}
	cur, _ := p.hashOf(&v)
	if verified != nil && cur != *verified {
		return errPasswordMoved
	}
	if err := p.fresh(ctx, key, cur, password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return failure(CodePasswordHash, "PASSWORD_HASH", "the password could not be hashed", err, errs.String("store", p.s.id))
	}
	return p.replace(ctx, key, cur, hash)
}

// fresh refuses, under NotReused(n), a password that the current hash or
// one of the n former ones verifies. All are verified whichever matches, and
// the refusal never says which.
func (p *PasswordPolicyService[T]) fresh(ctx context.Context, key, cur string, password []byte) error {
	if p.notReused == 0 {
		return nil
	}
	hashes, err := p.formerHashes(ctx, key)
	if err != nil {
		return err
	}
	if cur != "" {
		hashes = append([]string{cur}, hashes...)
	}
	if verifyAll(password, hashes) {
		return Invalid("this password was used recently: choose another one")
	}
	return nil
}

// formerHashes are the policy's field's n newest former hashes. A held
// record keeps more; the policy reads n.
func (p *PasswordPolicyService[T]) formerHashes(ctx context.Context, key string) ([]string, error) {
	h := p.s.historied()
	if h == nil {
		return nil, notRunning(&p.s.nodeBase)
	}
	all, err := h.formerAll(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range all[p.member.pointer] {
		var hash string
		if json.Unmarshal(e.Value, &hash) == nil && hash != "" {
			out = append(out, hash)
		}
		if len(out) == p.notReused {
			break
		}
	}
	return out, nil
}

// replace stores hash in the record under key if its hash is still was:
// errPasswordMoved otherwise.
func (p *PasswordPolicyService[T]) replace(ctx context.Context, key, was, hash string) error {
	_, err := p.s.modify(ctx, key, func(v *T) error {
		if cur, _ := p.hashOf(v); cur != was {
			return errPasswordMoved
		}
		p.setHash(v, hash)
		return nil
	})
	return err
}

// rehash stores the password again, hashed under the SDK's policy now: the
// same password, so the field's history records nothing. A failure leaves
// the older hash, which still verifies: kit logs it, and the login stands.
func (p *PasswordPolicyService[T]) rehash(ctx context.Context, key string, password []byte, was string) {
	hash, err := hashPassword(password)
	if err == nil {
		err = p.replace(withIntent(ctx, historyIntent{same: p.member.pointer}), key, was, hash)
	}
	if err != nil && !errors.Is(err, errPasswordMoved) {
		if a := p.s.app(); a != nil {
			logger.Warn(ctx, a.log, "a password's hash could not be upgraded", logger.String("store", p.s.id), logger.String("error", describeText(err)))
		}
	}
}

// verifyAll reports whether password matches any of hashes. It verifies
// every one — whichever matched, all are, so the time says nothing of
// which —, concurrently, at most GOMAXPROCS at once.
func verifyAll(password []byte, hashes []string) bool {
	var matched atomic.Bool
	var wg sync.WaitGroup
	slots := make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))
	for _, hash := range hashes {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			if ok, err := verifyPassword(password, hash); ok && err == nil {
				matched.Store(true)
			}
		})
	}
	wg.Wait()
	return matched.Load()
}

// dummyHash is the hash a missing record is verified against.
func dummyHash() string {
	d := dummy
	d.once.Do(func() {
		// A hash that cannot be made leaves "", which verifying refuses: the
		// caller then reports the hashing as broken.
		if h, err := hashPassword([]byte(rand.Text())); err == nil {
			d.hash = h
		}
	})
	return d.hash
}
