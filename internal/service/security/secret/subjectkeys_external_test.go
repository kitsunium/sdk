package secret_test

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/security/secret"
)

// rootName is the root secret every subject-key fixture wraps under — the
// name a framework gives its generated data key.
const rootName string = "data-key"

// cacheTTL is how long a caching engine in these tests keeps an opened key.
const cacheTTL time.Duration = time.Minute

// subjectFixture is one deployment: a manual clock, the root secret's store
// holding version 1, the keyring over it, and the store of wrapped keys. Each
// engine built over it is one PROCESS of that deployment.
type subjectFixture struct {
	clk   *clock.ManualClock
	roots coresecret.Store
	root  *svcsecret.Keyring
	store coresecret.SubjectKeyStore
}

// newSubjectFixture builds a deployment with a root at version 1.
func newSubjectFixture(t testing.TB) *subjectFixture {
	t.Helper()
	f := &subjectFixture{clk: clock.NewManualClock(epoch), store: svcsecret.NewMemorySubjectKeyStore()}
	f.roots = svcsecret.NewMemory(svcsecret.MemoryConfig{Clock: f.clk})
	putRandomVersion(t, f.roots, rootName)
	root, err := svcsecret.NewKeyring(f.roots, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	f.root = root
	return f
}

// process builds one engine over the deployment, caching up to size opened
// keys for cacheTTL on the fixture's clock; size 0 caches nothing.
func (f *subjectFixture) process(t *testing.T, size int) *svcsecret.SubjectKeys {
	t.Helper()
	cfg := svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheSize: size, Clock: f.clk}
	if size > 0 {
		cfg.CacheTTL = cacheTTL
	}
	keys, err := svcsecret.NewSubjectKeys(cfg)
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	return keys
}

// seal seals plaintext for subject, bound to bind, or fails the test.
func seal(t *testing.T, keys *svcsecret.SubjectKeys, subject, plaintext string, bind ...string) []byte {
	t.Helper()
	box, err := keys.Seal(t.Context(), subject, []byte(plaintext), bind...)
	if err != nil {
		t.Fatalf("Seal(%s): %v", subject, err)
	}
	return box
}

// mustOpen opens box and asserts its plaintext.
func mustOpen(t *testing.T, keys *svcsecret.SubjectKeys, box []byte, want string, bind ...string) {
	t.Helper()
	opened, err := keys.Open(t.Context(), box, bind...)
	if err != nil || string(opened) != want {
		t.Fatalf("Open = (%q, %v), want %q", opened, err, want)
	}
}

// openFails asserts that box does not open, with the given code.
func openFails(t *testing.T, keys *svcsecret.SubjectKeys, box []byte, code errs.Code, bind ...string) {
	t.Helper()
	opened, err := keys.Open(t.Context(), box, bind...)
	if opened != nil || !errs.HasCode(err, code) {
		t.Fatalf("Open = (%q, %v), want nothing and code %s", opened, err, code)
	}
}

// filed returns every key the store holds.
func filed(t *testing.T, store coresecret.SubjectKeyStore) []coresecret.SubjectKeyValue {
	t.Helper()
	var entries []coresecret.SubjectKeyValue
	for entry, err := range store.All(t.Context()) {
		if err != nil {
			t.Fatalf("All: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// TestSubjectKeysSealAndOpen is the round trip, and what a box says about
// itself: its subject in clear, its plaintext nowhere, one key per subject.
func TestSubjectKeysSealAndOpen(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	const plaintext = "jane.doe@example.org"
	first := seal(t, keys, "user:1", plaintext, "reports", "r-1", "/email")
	second := seal(t, keys, "user:1", "second value", "reports", "r-2", "/email")
	mustOpen(t, keys, first, plaintext, "reports", "r-1", "/email")
	mustOpen(t, keys, second, "second value", "reports", "r-2", "/email")
	if subject, err := svcsecret.SubjectOf(first); err != nil || subject != "user:1" {
		t.Fatalf("SubjectOf = (%q, %v), want user:1", subject, err)
	}
	if bytes.Contains(first, []byte(plaintext)) {
		t.Fatal("the box carries its plaintext")
	}
	entries := filed(t, f.store)
	if len(entries) != 1 || entries[0].Subject != "user:1" {
		t.Fatalf("store holds %d keys, want the one key of user:1", len(entries))
	}
	//: a wrapped key is a keyring box under the root's version 1.
	if wrapped := entries[0].Wrapped; len(wrapped) < 5 || wrapped[0] != 0x01 || wrapped[4] != 1 {
		t.Fatalf("wrapped key header = %x, want a keyring box under version 1", entries[0].Wrapped[:5])
	}
}

// TestSubjectKeysBindingIsWhereTheValueLies pins that a box opens only with
// the parts it was sealed with, in order, and that the length prefix keeps
// two splittings of one string apart.
func TestSubjectKeysBindingIsWhereTheValueLies(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	box := seal(t, keys, "user:1", "value", "reports", "r-1", "/email")
	split := seal(t, keys, "user:1", "value", "ab", "c")
	type tc struct {
		name string
		box  []byte
		bind []string
	}
	tests := []tc{
		{"another record", box, []string{"reports", "r-2", "/email"}},
		{"another field", box, []string{"reports", "r-1", "/name"}},
		{"another store", box, []string{"cases", "r-1", "/email"}},
		{"the parts reordered", box, []string{"r-1", "reports", "/email"}},
		{"a part missing", box, []string{"reports", "r-1"}},
		{"a part too many", box, []string{"reports", "r-1", "/email", ""}},
		{"no binding", box, nil},
		{"one string split another way", split, []string{"a", "bc"}},
		{"one string joined", split, []string{"abc"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		openFails(t, keys, c.box, coresecret.CodeSealInvalid, c.bind...)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSubjectKeysOpenIsNotAnOracle pins that no altered box ever opens, and
// that the verdicts are the two the doc names: SealInvalid for a malformed or
// altered box, KeyDestroyed where the header names a key that is not held —
// which an edited header cannot be told apart from, and says so.
func TestSubjectKeysOpenIsNotAnOracle(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	box := seal(t, keys, "user:1", "value")
	seal(t, keys, "user:2", "another subject holds a key")
	edit := func(index int, value byte) []byte {
		out := bytes.Clone(box)
		out[index] = value
		return out
	}
	idStart := 2 + len("user:1")
	type tc struct {
		name string
		box  []byte
		code errs.Code
	}
	tests := []tc{
		{"empty", nil, coresecret.CodeSealInvalid},
		{"the header alone", box[:idStart+8], coresecret.CodeSealInvalid},
		{"an unknown format", edit(0, 0x7f), coresecret.CodeSealInvalid},
		{"a zero subject length", edit(1, 0), coresecret.CodeSealInvalid},
		{"a subject length past the box", edit(1, 0xff), coresecret.CodeSealInvalid},
		{"a subject outside the grammar", edit(2, 'U'), coresecret.CodeSealInvalid},
		{"a flipped bit in the ciphertext", edit(len(box)-1, box[len(box)-1]^0x01), coresecret.CodeSealInvalid},
		{"a flipped bit in the nonce", edit(idStart+8+2, box[idStart+8+2]^0x01), coresecret.CodeSealInvalid},
		{"an edited key identifier", edit(idStart, box[idStart]^0x01), coresecret.CodeKeyDestroyed},
		{"the subject edited to one with a key", edit(idStart-1, '2'), coresecret.CodeKeyDestroyed},
		{"the subject edited to one without", edit(idStart-1, '9'), coresecret.CodeKeyDestroyed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		openFails(t, keys, c.box, c.code)
		if c.code == coresecret.CodeSealInvalid {
			if _, err := svcsecret.SubjectOf(c.box); err == nil && len(c.box) < idStart+9 {
				t.Errorf("%s: SubjectOf accepted a box with no AEAD part", c.name)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestSubjectKeysDestroyIsACryptographicErase is the engine's reason to
// exist: destroying a subject's key reaches every copy of every box sealed
// under it — the ones nobody rewrites — and nobody else's.
func TestSubjectKeysDestroyIsACryptographicErase(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	record := seal(t, keys, "user:1", "jane", "reports", "r-1", "/name")
	formerVersion := bytes.Clone(record)
	deadLetter := seal(t, keys, "user:1", "jane, queued", "queue")
	other := seal(t, keys, "user:2", "john", "reports", "r-2", "/name")
	destroyed, err := keys.Destroy(t.Context(), "user:1")
	if err != nil || !destroyed {
		t.Fatalf("Destroy = (%v, %v), want (true, nil)", destroyed, err)
	}
	openFails(t, keys, record, coresecret.CodeKeyDestroyed, "reports", "r-1", "/name")
	openFails(t, keys, formerVersion, coresecret.CodeKeyDestroyed, "reports", "r-1", "/name")
	openFails(t, keys, deadLetter, coresecret.CodeKeyDestroyed, "queue")
	mustOpen(t, keys, other, "john", "reports", "r-2", "/name")
	//: destroying again finds nothing, and says so without failing.
	if again, againErr := keys.Destroy(t.Context(), "user:1"); again || againErr != nil {
		t.Fatalf("second Destroy = (%v, %v), want (false, nil)", again, againErr)
	}
	//: the subject comes back: a NEW key, which opens nothing the old one sealed.
	fresh := seal(t, keys, "user:1", "jane, again", "reports", "r-3", "/name")
	mustOpen(t, keys, fresh, "jane, again", "reports", "r-3", "/name")
	openFails(t, keys, record, coresecret.CodeKeyDestroyed, "reports", "r-1", "/name")
}

// TestSubjectKeysDestroyReachesThisProcessAtOnce pins that a destruction
// empties the cache of the process that performs it — no TTL to wait out.
func TestSubjectKeysDestroyReachesThisProcessAtOnce(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 16)
	box := seal(t, keys, "user:1", "value")
	mustOpen(t, keys, box, "value")
	if _, err := keys.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	openFails(t, keys, box, coresecret.CodeKeyDestroyed)
}

// TestSubjectKeysCacheTTLBoundsAnotherProcessesErasure pins the bound the
// configuration promises: a key destroyed by ANOTHER process keeps opening in
// a process that cached it until its CacheTTL passes, and not a moment after.
func TestSubjectKeysCacheTTLBoundsAnotherProcessesErasure(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	reader, eraser := f.process(t, 16), f.process(t, 0)
	box := seal(t, reader, "user:1", "value")
	mustOpen(t, reader, box, "value")
	if _, err := eraser.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	openFails(t, eraser, box, coresecret.CodeKeyDestroyed)
	//: within the TTL the reader still holds the key — the documented bound.
	f.clk.Advance(cacheTTL - time.Second)
	mustOpen(t, reader, box, "value")
	f.clk.Advance(time.Second)
	openFails(t, reader, box, coresecret.CodeKeyDestroyed)
}

// TestSubjectKeysACachedKeyReplacedElsewhereIsReread pins that a key
// identifier the cache does not know sends Open to the store once: a subject
// erased and written again by another process is read under its new key, not
// reported as destroyed.
func TestSubjectKeysACachedKeyReplacedElsewhereIsReread(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	reader, writer := f.process(t, 16), f.process(t, 0)
	old := seal(t, reader, "user:1", "old")
	mustOpen(t, reader, old, "old")
	if _, err := writer.Destroy(t.Context(), "user:1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	fresh := seal(t, writer, "user:1", "fresh")
	mustOpen(t, reader, fresh, "fresh")
	openFails(t, reader, old, coresecret.CodeKeyDestroyed)
}

// TestSubjectKeysConcurrentFirstSealsAgreeOnOneKey pins the Insert contract
// from the engine's side: many first Seals of one subject, across goroutines
// and two processes, file ONE key, and every box opens under it.
func TestSubjectKeysConcurrentFirstSealsAgreeOnOneKey(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	processes := []*svcsecret.SubjectKeys{f.process(t, 16), f.process(t, 0)}
	const writers int = 32
	boxes := make([][]byte, writers)
	failures := make([]error, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Go(func() {
			boxes[index], failures[index] = processes[index%2].Seal(context.Background(), "user:1", []byte("value"))
		})
	}
	group.Wait()
	for index, failure := range failures {
		if failure != nil {
			t.Fatalf("writer %d: Seal: %v", index, failure)
		}
	}
	if entries := filed(t, f.store); len(entries) != 1 {
		t.Fatalf("store holds %d keys for one subject, want 1", len(entries))
	}
	for _, box := range boxes {
		mustOpen(t, processes[0], box, "value")
		mustOpen(t, processes[1], box, "value")
	}
}

// TestSubjectKeysAReplacedRootIsNotAnErasure pins that a key which does not
// unwrap — here, the root secret was replaced under the same version number —
// is a loud fault, never read as an erasure, and never overwritten by a Seal.
func TestSubjectKeysAReplacedRootIsNotAnErasure(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	box := seal(t, f.process(t, 0), "user:1", "value")
	before := filed(t, f.store)
	otherRoots := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	putRandomVersion(t, otherRoots, rootName)
	otherRoot, err := svcsecret.NewKeyring(otherRoots, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	misconfigured, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: otherRoot, Store: f.store})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	openFails(t, misconfigured, box, coresecret.CodeSubjectKeyUnreadable)
	if _, sealErr := misconfigured.Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, coresecret.CodeSubjectKeyUnreadable) {
		t.Fatalf("Seal over an unreadable key = %v, want SubjectKeyUnreadable", sealErr)
	}
	if after := filed(t, f.store); len(after) != 1 || !bytes.Equal(after[0].Wrapped, before[0].Wrapped) {
		t.Fatal("a Seal replaced a key it could not unwrap")
	}
	if !strings.Contains(fieldText(unreadableErr(t, misconfigured, box)), "subject=user:1") {
		t.Error("the verdict does not name the subject an operator must look at")
	}
}

// unreadableErr returns the error Open gives for box.
func unreadableErr(t *testing.T, keys *svcsecret.SubjectKeys, box []byte) error {
	t.Helper()
	_, err := keys.Open(t.Context(), box)
	return err
}

// TestSubjectKeysBeforeTheRootExists pins that a Seal before the root secret
// has a version reports the root's NotFound — Ensure it first — and files
// nothing.
func TestSubjectKeysBeforeTheRootExists(t *testing.T) {
	t.Parallel()
	roots := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	root, err := svcsecret.NewKeyring(roots, rootName)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	store := svcsecret.NewMemorySubjectKeyStore()
	keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: root, Store: store})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	if _, sealErr := keys.Seal(t.Context(), "user:1", []byte("x")); !errs.HasCode(sealErr, coresecret.CodeNotFound) {
		t.Fatalf("Seal without a root = %v, want NotFound", sealErr)
	}
	if entries := filed(t, store); len(entries) != 0 {
		t.Fatalf("store holds %d keys after a failed Seal", len(entries))
	}
}

// TestSubjectKeysRefuseAMalformedSubject pins that an identity passed where a
// reference belonged is refused before the store is asked, and not repeated.
func TestSubjectKeysRefuseAMalformedSubject(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	keys := f.process(t, 0)
	const identity = "Jane.Doe@example.org"
	_, sealErr := keys.Seal(t.Context(), identity, []byte("x"))
	_, destroyErr := keys.Destroy(t.Context(), identity)
	for _, err := range []error{sealErr, destroyErr} {
		if !errs.HasCode(err, coresecret.CodeInvalidSubject) {
			t.Fatalf("verdict = %v, want InvalidSubject", err)
		}
		if strings.Contains(err.Error()+errs.PrivateOf(err)+fieldText(err), identity) {
			t.Fatal("the refusal repeats the identity it refused")
		}
	}
	if entries := filed(t, f.store); len(entries) != 0 {
		t.Fatalf("store holds %d keys after refusals", len(entries))
	}
}

// TestNewSubjectKeysRefusesAnUnusableConfig pins every refusal, and that the
// cache's TTL is the caller's promise: required with a cache, refused without.
func TestNewSubjectKeysRefusesAnUnusableConfig(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	type tc struct {
		name    string
		cfg     svcsecret.SubjectKeysConfig
		setting string
	}
	tests := []tc{
		{"no root", svcsecret.SubjectKeysConfig{Store: f.store}, "Root"},
		{"no store", svcsecret.SubjectKeysConfig{Root: f.root}, "Store"},
		{"a negative cache", svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheSize: -1}, "CacheSize"},
		{"a cache that never expires", svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheSize: 8}, "CacheTTL"},
		{"a negative TTL", svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheSize: 8, CacheTTL: -time.Second}, "CacheTTL"},
		{"a TTL without a cache", svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheTTL: time.Minute}, "CacheTTL"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		keys, err := svcsecret.NewSubjectKeys(c.cfg)
		if keys != nil || !errs.HasCode(err, coresecret.CodeInvalidConfig) {
			t.Fatalf("%s: NewSubjectKeys = (%v, %v), want InvalidConfig", c.name, keys, err)
		}
		if !strings.Contains(fieldText(err), "setting="+c.setting) {
			t.Errorf("%s: refusal fields %q do not name %s", c.name, fieldText(err), c.setting)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// errBackendDown is the plain error a failing store double returns.
var errBackendDown = errors.New("backend down")

// failingKeyStore is a SubjectKeyStore whose backend fails every call with
// its err.
type failingKeyStore struct {
	err error
}

// Get fails.
func (s failingKeyStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, s.err
}

// Insert fails.
func (s failingKeyStore) Insert(context.Context, string, []byte) (bool, error) {
	return false, s.err
}

// Replace fails.
func (s failingKeyStore) Replace(context.Context, string, []byte, []byte) (bool, error) {
	return false, s.err
}

// Delete fails.
func (s failingKeyStore) Delete(context.Context, string) (bool, error) {
	return false, s.err
}

// All fails at once.
func (s failingKeyStore) All(context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
	return func(yield func(coresecret.SubjectKeyValue, error) bool) {
		//: one failure, then the sequence ends whatever the caller answers.
		if !yield(coresecret.SubjectKeyValue{}, s.err) {
			return
		}
	}
}

// TestSubjectKeysReportAStoreFailureAsItself pins that an error from the
// caller's store is the retryable StoreUnavailable — never KeyDestroyed,
// which would tell a caller to read a value as erased during an outage — and
// keeps the store's own error: reachable through errors.Is when it is plain,
// its code kept when it is an SDK error.
func TestSubjectKeysReportAStoreFailureAsItself(t *testing.T) {
	t.Parallel()
	f := newSubjectFixture(t)
	box := seal(t, f.process(t, 0), "user:1", "value")
	sdkCause := errs.Wrap(coresecret.ReadOnly, errs.WrapParams{})
	for _, cause := range []error{errBackendDown, sdkCause} {
		keys, err := svcsecret.NewSubjectKeys(svcsecret.SubjectKeysConfig{Root: f.root, Store: failingKeyStore{err: cause}})
		if err != nil {
			t.Fatalf("NewSubjectKeys: %v", err)
		}
		ctx := t.Context()
		_, sealErr := keys.Seal(ctx, "user:1", []byte("x"))
		_, openErr := keys.Open(ctx, box)
		_, destroyErr := keys.Destroy(ctx, "user:1")
		_, rewrapErr := keys.Rewrap(ctx)
		_, oldestErr := keys.OldestRoot(ctx)
		verdicts := []struct {
			call string
			err  error
		}{{"Seal", sealErr}, {"Open", openErr}, {"Destroy", destroyErr}, {"Rewrap", rewrapErr}, {"OldestRoot", oldestErr}}
		for _, verdict := range verdicts {
			if !errs.HasCode(verdict.err, coresecret.CodeStoreUnavailable) || errs.HasCode(verdict.err, coresecret.CodeKeyDestroyed) {
				t.Errorf("%s over a failing store = %v, want StoreUnavailable and never KeyDestroyed", verdict.call, verdict.err)
			}
			if cause == errBackendDown && !errors.Is(verdict.err, errBackendDown) {
				t.Errorf("%s: the store's own error is not in the chain", verdict.call)
			}
			if cause == sdkCause && !errs.HasCode(verdict.err, coresecret.CodeReadOnly) {
				t.Errorf("%s: the store's own code was lost", verdict.call)
			}
		}
	}
}
