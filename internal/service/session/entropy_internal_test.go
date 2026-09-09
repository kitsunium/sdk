package session

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// These tests are INTERNAL because the random source is deliberately not on the
// exported Config: a knob letting a caller swap the source of every session
// identifier is a footgun with no legitimate production use. It is still a
// field, and the collision guard it exists to make reachable is one of the few
// places where the safe answer is to refuse rather than to carry on — so the
// guard is asserted from inside the package rather than left as an argument.

// constantReader is what a broken entropy source looks like: it always answers
// with the same bytes and never reports a problem.
type constantReader struct{ fill byte }

// Read fills p with the constant and reports success.
func (c constantReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.fill
	}
	return len(p), nil
}

// failingReader is a random source that is down.
type failingReader struct{}

// Read always fails.
func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy pool unavailable") //nolint:err113 // a test double standing in for a failing io.Reader, not SDK code
}

// shortReader delivers half the bytes and then stops, which is what makes
// io.ReadFull rather than Read the load-bearing choice in mintID.
type shortReader struct{}

// Read fills half of p and reports EOF.
func (shortReader) Read(p []byte) (int, error) {
	half := len(p) / 2
	for i := range half {
		p[i] = 0x5A
	}
	return half, io.EOF
}

// testWindow is the policy every store in this file is built with.
func testWindow() Config {
	return Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 100 * time.Minute}
}

// internalKey is the AEAD key the file store in this file seals with.
func internalKey(t *testing.T) corecrypto.Key {
	t.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	key, err := corecrypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// TestABrokenRandomSourceIsRefusedNeverReused is the guard that keeps a
// degraded entropy source from silently disabling the whole domain.
//
// At 256 bits a collision cannot happen by chance, so one means the source is
// repeating itself. Reusing the identifier would hand one caller's session to
// another — and, worse, would make Regenerate a no-op, quietly reinstating the
// fixation hole it exists to close. Refusing is the only safe answer, and it is
// loud.
func TestABrokenRandomSourceIsRefusedNeverReused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(t *testing.T, source io.Reader) coresession.Store
	}{
		{"memory", buildMemoryWith},
		{"file", buildFileWith},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := tc.build(t, constantReader{fill: 0xAA})
			first, err := store.New(ctx)
			if err != nil {
				t.Fatalf("first New: %v", err)
			}
			//: the second mint repeats the first identifier.
			second, err := store.New(ctx)
			if !errs.HasCode(err, coresession.CodeIdentifierCollision) {
				t.Fatalf("second New = (%v, %v), want CodeIdentifierCollision", second, err)
			}
			if !second.IsZero() {
				t.Error("a refused New returned a session anyway")
			}
			//: and the first session is untouched — nothing was overwritten.
			loaded, loadErr := store.Load(ctx, first.ID())
			if loadErr != nil || !loaded.ID().Equal(first.ID()) {
				t.Fatalf("the original session did not survive: (%v, %v)", loaded, loadErr)
			}
			//: Regenerate refuses for the same reason. This is the important
			//: half: a rotation that "succeeded" onto the same identifier would
			//: be a login that did not rotate.
			rotated, regenErr := store.Regenerate(ctx, first.ID(), "alice")
			if !errs.HasCode(regenErr, coresession.CodeIdentifierCollision) {
				t.Fatalf("Regenerate = (%v, %v), want CodeIdentifierCollision", rotated, regenErr)
			}
			//: and the session is still anonymous, because nothing was written.
			after, afterErr := store.Load(ctx, first.ID())
			if afterErr != nil {
				t.Fatalf("Load: %v", afterErr)
			}
			if after.Subject() != "" {
				t.Errorf("Subject = %q after a refused rotation, want anonymous", after.Subject())
			}
		})
	}
}

// TestNoIdentifierIsMintedFromPartialEntropy pins io.ReadFull's role. A short
// read accepted as a smaller identifier would silently reduce the entropy the
// whole domain rests on, in exactly the situation where that matters most.
func TestNoIdentifierIsMintedFromPartialEntropy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source io.Reader
	}{
		{"the source is down", failingReader{}},
		{"the source is short", shortReader{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := buildMemoryWith(t, tc.source)
			fresh, err := store.New(ctx)
			if !errs.HasCode(err, coresession.CodeEntropyFailed) {
				t.Fatalf("New = (%v, %v), want CodeEntropyFailed", fresh, err)
			}
			if !fresh.IsZero() {
				t.Error("a refused New returned a session anyway")
			}
		})
	}
}

// buildMemoryWith builds a memory store on a chosen random source.
func buildMemoryWith(t *testing.T, source io.Reader) coresession.Store {
	t.Helper()
	cfg := testWindow()
	cfg.Clock = clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC))
	store, err := NewMemoryStore(cfg)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	concrete, ok := store.(*memoryStore)
	if !ok {
		t.Fatalf("NewMemoryStore returned %T, want *memoryStore", store)
	}
	concrete.source = source
	return store
}

// buildFileWith builds a file store on a chosen random source, skipping where
// the file store honestly refuses to exist.
func buildFileWith(t *testing.T, source io.Reader) coresession.Store {
	t.Helper()
	base := testWindow()
	store, err := NewFileStore(FileConfig{
		IdleTimeout: base.IdleTimeout, AbsoluteTimeout: base.AbsoluteTimeout,
		Clock: clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)),
		Dir:   filepath.Join(t.TempDir(), "sessions"), Key: internalKey(t),
	})
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		t.Skip("file store has no native mechanic on this platform")
	}
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	concrete, ok := store.(*fileStore)
	if !ok {
		t.Fatalf("NewFileStore returned %T, want *fileStore", store)
	}
	concrete.source = source
	return store
}
