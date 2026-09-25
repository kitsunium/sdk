package secret_test

import (
	"bytes"
	"testing"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// keyringFixture is a memory store holding one fresh 32-byte version of
// "session-key", and the keyring over it.
func keyringFixture(t *testing.T) (coresecret.Store, *svcsecret.Keyring) {
	t.Helper()
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	putRandomVersion(t, store, "session-key")
	keyring, err := svcsecret.NewKeyring(store, "session-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return store, keyring
}

// putRandomVersion stores a new 32-byte random version of name.
func putRandomVersion(t *testing.T, store coresecret.Store, name string) coresecret.VersionValue {
	t.Helper()
	value, err := svcsecret.Random(32)()
	if err != nil {
		t.Fatalf("Random: %v", err)
	}
	created, err := store.Put(t.Context(), name, value)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return created
}

// TestKeyringOpensWhatWasSealedBeforeARotation is the keyring's reason to
// exist: a rotation never breaks a box sealed before it, until the version
// that sealed it is pruned — and then the box is refused.
func TestKeyringOpensWhatWasSealedBeforeARotation(t *testing.T) {
	t.Parallel()
	store, keyring := keyringFixture(t)
	ctx := t.Context()
	before, err := keyring.Seal(ctx, []byte("sealed under v1"), []byte("aad"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	signedBefore, err := keyring.Sign(ctx, []byte("signed under v1"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	//: rotate twice; the newest seals, and v1 is still kept.
	putRandomVersion(t, store, "session-key")
	putRandomVersion(t, store, "session-key")
	after, err := keyring.Seal(ctx, []byte("sealed under v3"), nil)
	if err != nil {
		t.Fatalf("Seal after rotation: %v", err)
	}
	if bytes.Equal(before[:5], after[:5]) {
		t.Fatalf("a box sealed after the rotation carries the old header %x", after[:5])
	}
	for _, c := range []struct {
		box       []byte
		aad, want string
	}{{before, "aad", "sealed under v1"}, {after, "", "sealed under v3"}} {
		opened, openErr := keyring.Open(ctx, c.box, []byte(c.aad))
		if openErr != nil || string(opened) != c.want {
			t.Fatalf("Open = (%q, %v), want %q", opened, openErr, c.want)
		}
	}
	if verifyErr := keyring.Verify(ctx, []byte("signed under v1"), signedBefore); verifyErr != nil {
		t.Fatalf("Verify after rotation: %v", verifyErr)
	}
	//: prune v1: what it sealed and signed is refused from now on.
	if pruneErr := store.Prune(ctx, "session-key", 2); pruneErr != nil {
		t.Fatalf("Prune: %v", pruneErr)
	}
	if _, openErr := keyring.Open(ctx, before, []byte("aad")); !errs.HasCode(openErr, svcsecret.CodeSealInvalid) {
		t.Errorf("Open of a box sealed under a pruned version = %v, want SealInvalid", openErr)
	}
	if verifyErr := keyring.Verify(ctx, []byte("signed under v1"), signedBefore); !errs.HasCode(verifyErr, svcsecret.CodeSignatureInvalid) {
		t.Errorf("Verify under a pruned version = %v, want SignatureInvalid", verifyErr)
	}
	if opened, openErr := keyring.Open(ctx, after, nil); openErr != nil || string(opened) != "sealed under v3" {
		t.Errorf("Open of the newest box after the prune = (%q, %v)", opened, openErr)
	}
}

// TestKeyringOpenIsNotAnOracle pins that every way a box can be wrong is one
// verdict.
func TestKeyringOpenIsNotAnOracle(t *testing.T) {
	t.Parallel()
	_, keyring := keyringFixture(t)
	box, err := keyring.Seal(t.Context(), []byte("payload"), []byte("bound"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	flipped := bytes.Clone(box)
	flipped[len(flipped)-1] ^= 0x01
	renumbered := bytes.Clone(box)
	renumbered[4] = 0x02
	otherFormat := bytes.Clone(box)
	otherFormat[0] = 0x7f
	type tc struct {
		name string
		box  []byte
		aad  string
	}
	tests := []tc{
		{"empty", nil, "bound"},
		{"a header alone", box[:5], "bound"},
		{"a flipped bit", flipped, "bound"},
		{"a version edited in the header", renumbered, "bound"},
		{"an unknown format", otherFormat, "bound"},
		{"other associated data", box, "other"},
		{"no associated data", box, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if _, openErr := keyring.Open(t.Context(), c.box, []byte(c.aad)); !errs.HasCode(openErr, svcsecret.CodeSealInvalid) {
			t.Errorf("%s: Open = %v, want SealInvalid", c.name, openErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestKeyringBindsItsName pins that two keyrings over identical key material
// do not open each other's boxes or verify each other's signatures.
func TestKeyringBindsItsName(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	shared := putRandomVersion(t, store, "first")
	if _, err := store.Put(t.Context(), "second", shared.Value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first, err := svcsecret.NewKeyring(store, "first")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	second, err := svcsecret.NewKeyring(store, "second")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	box, err := first.Seal(t.Context(), []byte("for first"), nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, openErr := second.Open(t.Context(), box, nil); !errs.HasCode(openErr, svcsecret.CodeSealInvalid) {
		t.Errorf("another keyring opened the box: %v", openErr)
	}
	signature, err := first.Sign(t.Context(), []byte("message"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if verifyErr := second.Verify(t.Context(), []byte("message"), signature); !errs.HasCode(verifyErr, svcsecret.CodeSignatureInvalid) {
		t.Errorf("another keyring verified the signature: %v", verifyErr)
	}
	if verifyErr := first.Verify(t.Context(), []byte("massage"), signature); !errs.HasCode(verifyErr, svcsecret.CodeSignatureInvalid) {
		t.Errorf("a signature verified another message: %v", verifyErr)
	}
}

// TestKeyringRefusals pins the construction refusals and the key-material
// rule: a version that is not exactly one crypto.Key long is not a key.
func TestKeyringRefusals(t *testing.T) {
	t.Parallel()
	store := svcsecret.NewMemory(svcsecret.MemoryConfig{})
	if _, err := svcsecret.NewKeyring(nil, "k"); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
		t.Errorf("NewKeyring(nil store) = %v, want InvalidConfig", err)
	}
	if _, err := svcsecret.NewKeyring(store, "Not A Name"); !errs.HasCode(err, coresecret.CodeInvalidName) {
		t.Errorf("NewKeyring(bad name) = %v, want InvalidName", err)
	}
	keyring, err := svcsecret.NewKeyring(store, "password-not-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if _, sealErr := keyring.Seal(t.Context(), []byte("x"), nil); !errs.HasCode(sealErr, coresecret.CodeNotFound) {
		t.Errorf("Seal before any version = %v, want NotFound", sealErr)
	}
	if _, putErr := store.Put(t.Context(), "password-not-key", coresecret.FromString("correct horse")); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	if _, sealErr := keyring.Seal(t.Context(), []byte("x"), nil); !errs.HasCode(sealErr, svcsecret.CodeKeyMaterialInvalid) {
		t.Errorf("Seal under a password = %v, want KeyMaterialInvalid", sealErr)
	}
	if _, signErr := keyring.Sign(t.Context(), []byte("x")); !errs.HasCode(signErr, svcsecret.CodeKeyMaterialInvalid) {
		t.Errorf("Sign under a password = %v, want KeyMaterialInvalid", signErr)
	}
}
