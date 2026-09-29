package kit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The password policy (ADR 0007 §2), on a hashing of the tests' own: the
// SDK's costs 144 ms by design — seconds under the race detector —, and the
// tests count what the policy verifies. passwords_test.go runs it once on
// the SDK's.

// fakeHashing is a fast hashing that counts: "$test$<salt>$<sum>", and
// "$old$…" for a hash NeedsRehash says is weaker.
type fakeHashing struct {
	hashes, verifies atomic.Int32
	inFlight, most   atomic.Int32
	// delay is how long a verification takes, so that several overlap.
	delay time.Duration
}

// useFakeHashing replaces the SDK's hashing for the test.
func useFakeHashing(t *testing.T) *fakeHashing {
	t.Helper()
	f := &fakeHashing{}
	h, v, n, d := hashPassword, verifyPassword, needsRehash, dummy
	hashPassword, verifyPassword, dummy = f.hash, f.verify, &dummyHashes{}
	needsRehash = func(phc string) bool { return strings.HasPrefix(phc, "$old$") }
	t.Cleanup(func() { hashPassword, verifyPassword, needsRehash, dummy = h, v, n, d })
	return f
}

// fakeHash is the hash of pw under a scheme and a salt.
func fakeHash(scheme string, salt, pw []byte) string {
	sum := sha256.Sum256(append(append([]byte{}, salt...), pw...))
	return "$" + scheme + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func (f *fakeHashing) hash(pw []byte) (string, error) {
	f.hashes.Add(1)
	salt := make([]byte, 8)
	_, _ = rand.Read(salt)
	return fakeHash("test", salt, pw), nil
}

func (f *fakeHashing) verify(pw []byte, phc string) (bool, error) {
	f.verifies.Add(1)
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for m := f.most.Load(); n > m && !f.most.CompareAndSwap(m, n); m = f.most.Load() {
	}
	time.Sleep(f.delay)
	parts := strings.Split(phc, "$")
	if len(parts) != 4 || (parts[1] != "test" && parts[1] != "old") {
		return false, errors.New("not a test hash")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, err
	}
	return fakeHash(parts[1], salt, pw) == phc, nil
}

// counts are how many hashes and verifications the policy ran since the
// last call.
func (f *fakeHashing) counts() (hashes, verifies int32) {
	return f.hashes.Swap(0), f.verifies.Swap(0)
}

// lockAccount is an account whose password a policy keeps.
type lockAccount struct {
	ID       string `json:"id"`
	Logins   int    `json:"logins"`
	Password string `json:"password,omitempty" kit:"secret"`
}

// locks is a store of accounts under a policy, in a running app.
type locks struct {
	app      *App
	accounts *StoreService[lockAccount]
	policy   *PasswordPolicyService[lockAccount]
}

// startLocks runs a store of accounts whose policy has opts, in dev, in
// memory unless appOpts say where.
func startLocks(t *testing.T, name string, opts []PasswordConfigurer, appOpts ...AppConfigurer) locks {
	t.Helper()
	svc := NewService(name, "Accounts under a password policy, for the tests.")
	l := locks{accounts: svc.Store("accounts", func(a lockAccount) string { return a.ID })}
	l.policy = l.accounts.Passwords(func(a *lockAccount) *string { return &a.Password }, opts...)
	if len(appOpts) == 0 {
		appOpts = []AppConfigurer{InMemory()}
	}
	l.app = NewApp(name, svc).With(append([]AppConfigurer{
		Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard),
	}, appOpts...)...)
	if err := l.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := l.app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	return l
}

// must fails the test on an error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// isInvalid reports whether err is kit's Invalid.
func isInvalid(err error) bool {
	var ke *Error
	return errors.As(err, &ke) && ke.Code == WireInvalid
}

// pw is a password of the default length, 15 characters at least.
func pw(word string) []byte { return []byte("correct horse " + word) }

// refusals sets each word again, and returns what each refusal said and
// the work it did: "hashes/verifications".
func refusals(t *testing.T, l locks, f *fakeHashing, words ...string) (said, work []string) {
	t.Helper()
	f.counts()
	for _, word := range words {
		err := l.policy.Set(t.Context(), "a1", pw(word))
		if !isInvalid(err) {
			t.Fatalf("%s was accepted again: %v", word, err)
		}
		hashes, verifies := f.counts()
		said, work = append(said, err.Error()), append(work, fmt.Sprintf("%d/%d", hashes, verifies))
	}
	return said, work
}

// The current password and each of the last n are refused, with the same
// words and after the same work — every hash verified, whichever matched —;
// the (n+1)-th back is accepted again.
func TestAPasswordIsNotReused(t *testing.T) {
	f := useFakeHashing(t)
	l := startLocks(t, "locks-reuse", []PasswordConfigurer{NotReused(2)})
	ctx := t.Context()
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1"}))
	for _, word := range []string{"one", "two", "three"} {
		must(t, l.policy.Set(ctx, "a1", pw(word)))
	}
	said, work := refusals(t, l, f, "three", "two", "one")
	if said[0] != said[1] || said[1] != said[2] || !strings.Contains(said[0], "used recently") {
		t.Errorf("the refusals say which one matched: %q", said)
	}
	if !slices.Equal(work, []string{"0/3", "0/3", "0/3"}) {
		t.Errorf("the refusals did %q, want no hash and three verifications each", work)
	}
	must(t, l.policy.Set(ctx, "a1", pw("four")))
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	if ok, err := l.policy.Verify(ctx, "a1", pw("one")); !ok || err != nil {
		t.Errorf("the password set is not the account's: %v %v", ok, err)
	}
}

// Without NotReused, nothing is verified, and the current password may be
// set again.
func TestReuseIsOffByDefault(t *testing.T) {
	f := useFakeHashing(t)
	l := startLocks(t, "locks-default", nil)
	ctx := t.Context()
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1"}))
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	f.counts()
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	if hashes, verifies := f.counts(); hashes != 1 || verifies != 0 {
		t.Errorf("a set hashed %d and verified %d, want 1 and 0", hashes, verifies)
	}
	if h := l.app.Graph().Node("locks-default/store/accounts").Store.History; h == nil || len(h.Fields) != 0 ||
		len(h.Passwords) != 1 || h.Passwords[0].MinLength != 15 || h.Passwords[0].NotReused != 0 {
		t.Errorf("the model: %+v", h)
	}
}

// Every former hash is verified, whichever matches, concurrently and at
// most GOMAXPROCS at once.
func TestTheVerificationsRunConcurrently(t *testing.T) {
	f := useFakeHashing(t)
	f.delay = 20 * time.Millisecond
	hashes := make([]string, 6)
	for i := range hashes {
		var hashesErr error
		hashes[i], hashesErr = f.hash(pw(string(rune('a' + i))))
		if hashesErr != nil {
			t.Fatal(hashesErr)
		}
	}
	for _, procs := range []int{1, 2, 4} {
		prev := runtime.GOMAXPROCS(procs)
		f.counts()
		f.most.Store(0)
		matched := verifyAll(pw("a"), hashes)
		runtime.GOMAXPROCS(prev)
		if _, verifies := f.counts(); !matched || verifies != int32(len(hashes)) {
			t.Errorf("GOMAXPROCS %d: matched %v after %d verifications, want true after %d", procs, matched, verifies, len(hashes))
		}
		if most := f.most.Load(); most != int32(procs) {
			t.Errorf("GOMAXPROCS %d: %d verifications at once", procs, most)
		}
	}
}

// Change verifies the current password first; Set on a record that is not
// there is a NotFound.
func TestChangeAsksForTheCurrentPassword(t *testing.T) {
	useFakeHashing(t)
	l := startLocks(t, "locks-change", []PasswordConfigurer{NotReused(1)})
	ctx := t.Context()
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1"}))
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	if err := l.policy.Change(ctx, "a1", pw("wrong"), pw("two")); !isInvalid(err) || !strings.Contains(err.Error(), "current password") {
		t.Errorf("a wrong current password: %v", err)
	}
	must(t, l.policy.Change(ctx, "a1", pw("one"), pw("two")))
	if err := l.policy.Change(ctx, "a1", pw("two"), pw("one")); !isInvalid(err) {
		t.Errorf("the former password came back: %v", err)
	}
	if ok, err := l.policy.Verify(ctx, "a1", pw("two")); err != nil || !ok {
		t.Error("the new password does not verify")
	}
	if err := l.policy.Set(ctx, "nobody", pw("one")); !isNotFound(err) {
		t.Errorf("a missing record: %v", err)
	}
	if err := l.policy.Set(ctx, "a1", []byte("too short")); !isInvalid(err) || !strings.Contains(err.Error(), "15") {
		t.Errorf("a short password: %v", err)
	}
}

// A login verifies once, whatever the key: a missing record, or one with no
// password yet, costs what a wrong password costs, and answers false. A
// weaker hash is stored again at a login, and the rehash is no former
// value.
func TestVerifyCostsTheSameAndRehashes(t *testing.T) {
	f := useFakeHashing(t)
	l := startLocks(t, "locks-verify", []PasswordConfigurer{NotReused(2)})
	ctx := t.Context()
	old := fakeHash("old", []byte("salt"), pw("one"))
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1", Password: old}))
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a2"}))
	dummyHash()
	f.counts()
	for _, c := range []struct {
		key, word string
		want      bool
	}{{"a1", "wrong", false}, {"nobody", "one", false}, {"a2", "one", false}, {"a1", "one", true}} {
		ok, err := l.policy.Verify(ctx, c.key, pw(c.word))
		if ok != c.want || err != nil {
			t.Errorf("%s with %s: %v %v", c.key, c.word, ok, err)
		}
		if _, verifies := f.counts(); verifies != 1 {
			t.Errorf("%s with %s verified %d times, want once", c.key, c.word, verifies)
		}
	}
	a, aErr := l.accounts.Get(ctx, "a1")
	if aErr != nil {
		t.Fatal(aErr)
	}
	if !strings.HasPrefix(a.Password, "$test$") {
		t.Errorf("the weaker hash was not stored again: %s", a.Password)
	}
	if former, err := l.accounts.Former(ctx, "a1", "/password"); err != nil || len(former) != 0 {
		t.Errorf("a rehash left a former value: %+v %v", former, err)
	}
}
