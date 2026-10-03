package secret_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// TestSubjectKeysThroughTheFacade runs the wiring the package doc shows,
// through public names only: a rotated root whose rotator re-wraps after each
// rotation and never prunes a version a key needs, a box that survives three
// rotations, and an erasure that ends it.
func TestSubjectKeysThroughTheFacade(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewManualClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	secrets := secret.NewMemory(secret.MemoryConfig{Clock: clk})
	root, err := secret.NewKeyring(secrets, "data-key")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	keys, err := secret.NewSubjectKeys(secret.SubjectKeysConfig{
		Root: root, Store: secret.NewMemorySubjectKeyStore(),
		CacheSize: 64, CacheTTL: time.Minute, Clock: clk,
	})
	if err != nil {
		t.Fatalf("NewSubjectKeys: %v", err)
	}
	var mu sync.Mutex
	var reports []secret.RewrapReport
	rotator, err := secret.NewRotator(secret.RotatorConfig{
		Store: secrets, Name: "data-key", Clock: clk,
		Policy: secret.Policy{Every: time.Hour, Keep: 2, Generate: secret.Random(32)},
		InUse:  keys.OldestRoot,
		OnRotate: func(secret.Versioned) {
			report, rewrapErr := keys.Rewrap(ctx)
			if rewrapErr != nil {
				t.Errorf("Rewrap: %v", rewrapErr)
			}
			mu.Lock()
			defer mu.Unlock()
			reports = append(reports, report)
		},
	})
	if err != nil {
		t.Fatalf("NewRotator: %v", err)
	}
	if _, err := rotator.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	const ref = "4f2a9c"
	box, err := keys.Seal(ctx, ref, []byte("jane.doe@example.org"), "reports", "r-1", "/email")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if subject, err := secret.SubjectOf(box); err != nil || subject != ref {
		t.Fatalf("SubjectOf = (%q, %v), want %q", subject, err, ref)
	}
	for range 3 {
		clk.Advance(time.Hour)
		if _, rotated, err := rotator.RotateIfDue(ctx); err != nil || !rotated {
			t.Fatalf("RotateIfDue = (%v, %v), want a rotation", rotated, err)
		}
	}
	mu.Lock()
	moved := len(reports) == 3 && reports[2] == (secret.RewrapReport{Root: 4, Rewrapped: 1})
	mu.Unlock()
	if !moved {
		t.Fatalf("rewrap reports = %+v, want the key moved at every rotation", reports)
	}
	opened, err := keys.Open(ctx, box, "reports", "r-1", "/email")
	if err != nil || string(opened) != "jane.doe@example.org" {
		t.Fatalf("Open after three rotations = (%q, %v)", opened, err)
	}
	if destroyed, err := keys.Destroy(ctx, ref); err != nil || !destroyed {
		t.Fatalf("Destroy = (%v, %v)", destroyed, err)
	}
	if _, err := keys.Open(ctx, box, "reports", "r-1", "/email"); !errs.HasCode(err, secret.KeyDestroyed.Code()) {
		t.Fatalf("Open after Destroy = %v, want KeyDestroyed", err)
	}
	if err := secret.ValidateSubject("Jane@Example"); !errs.HasCode(err, secret.InvalidSubject.Code()) {
		t.Fatalf("ValidateSubject of an identity = %v, want InvalidSubject", err)
	}
	if _, err := secret.NewSubjectKeys(secret.SubjectKeysConfig{Root: root, Store: secret.NewMemorySubjectKeyStore(), CacheSize: 8}); !errs.HasCode(err, secret.InvalidConfig.Code()) {
		t.Fatalf("NewSubjectKeys with a cache and no TTL = %v, want InvalidConfig", err)
	}
	if !errs.HasCode(secret.SubjectKeyUnreadable, secret.SubjectKeyUnreadable.Code()) || secret.MaxSubjectLen != 128 {
		t.Fatal("the facade's sentinel or bound drifted from the core")
	}
}
