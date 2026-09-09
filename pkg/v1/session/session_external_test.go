package session_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/session"
)

// TestTheConsumerLifecycle is the shape a consumer actually writes: a visitor
// arrives, browses, logs in, is served, and logs out. It exercises the facade's
// delegation rather than re-testing the stores, which internal/service/session
// covers exhaustively against both backends.
func TestTheConsumerLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := session.NewMemoryStore(session.Config{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	visitor, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = store.Save(ctx, visitor.Set("cart", "1 item")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	//: logging in. There is no other spelling of this, and it rotates.
	member, err := store.Regenerate(ctx, visitor.ID(), "user-42")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if member.ID().Equal(visitor.ID()) {
		t.Fatal("the identifier survived a login")
	}
	if value, _ := member.Get("cart"); value != "1 item" {
		t.Errorf("cart = %q, want it carried across the login", value)
	}
	if _, err = store.Load(ctx, visitor.ID()); !errs.HasReason(err, "NOT_FOUND") {
		t.Errorf("the pre-login identifier still resolves: %v", err)
	}
	//: logging out takes effect on the next request, not at the next expiry.
	if err = store.Destroy(ctx, member.ID()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err = store.Load(ctx, member.ID()); !errs.HasReason(err, "NOT_FOUND") {
		t.Errorf("Load after Destroy = %v, want NOT_FOUND", err)
	}
}

// TestTheCookieRoundTrip pins the boundary this domain draws: the SDK produces
// the cookie's VALUE and parses it back, and the framework does everything
// either side of that.
func TestTheCookieRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := session.NewMemoryStore(session.Config{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	sealer, err := session.NewSealer(testKey(t), "example.test/sid")
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	fresh, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	//: what the framework would put in a Set-Cookie header.
	value, err := sealer.Seal(fresh.ID())
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	//: and what it does with the header it gets back.
	id, err := sealer.Open(value)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	loaded, err := store.Load(ctx, id)
	if err != nil || !loaded.ID().Equal(fresh.ID()) {
		t.Fatalf("Load(Open(Seal(id))) = (%v, %v), want the same session", loaded, err)
	}
	//: the unsealed path exists too — an identifier is already opaque, and a
	//: framework that does not want the AEAD layer parses it directly.
	direct, err := session.ParseID(fresh.ID().Reveal())
	if err != nil || !direct.Equal(fresh.ID()) {
		t.Fatalf("ParseID(Reveal()) = (%v, %v), want the same identifier", direct, err)
	}
}

// TestSentinelsAreMatchableThroughPkgErrs pins that the re-exported sentinels
// are the same values the stores emit. An alias that had drifted to a copy
// would leave every consumer's errs.HasCode silently false.
func TestSentinelsAreMatchableThroughPkgErrs(t *testing.T) {
	t.Parallel()
	_, err := session.NewMemoryStore(session.Config{})
	if !errs.HasCode(err, mustCode(t, session.InvalidConfig)) {
		t.Errorf("NewMemoryStore(zero) = %v, want the re-exported InvalidConfig code", err)
	}
	if !errs.HasReason(err, "INVALID_CONFIG") {
		t.Errorf("NewMemoryStore(zero) = %v, want reason INVALID_CONFIG", err)
	}
	//: every "no usable session" verdict routes to 401, so a framework maps the
	//: whole family with one call instead of a switch it has to keep in sync.
	for _, sentinel := range []error{
		session.NotFound, session.Expired, session.InvalidID,
		session.SealInvalid, session.RecordCorrupt,
	} {
		if status := errs.HTTPStatusOf(sentinel); status != 401 {
			t.Errorf("%v maps to HTTP %d, want 401", sentinel, status)
		}
	}
}

// TestTheFileStoreRefusesOrWorks pins that NewFileStore is reachable through
// the facade and answers one of exactly two ways: a working store, or the
// SDK-wide UnsupportedPlatform. It never returns a store that pretends.
func TestTheFileStoreRefusesOrWorks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := session.NewFileStore(session.FileConfig{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
		Dir: filepath.Join(t.TempDir(), "sessions"), Key: testKey(t),
	})
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		//: the honest refusal. Nothing else is asserted, because nothing else
		//: exists on such a platform.
		if store != nil {
			t.Error("UnsupportedPlatform came back with a store attached")
		}
		return
	}
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fresh, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err = store.Load(ctx, fresh.ID()); err != nil {
		t.Errorf("Load: %v", err)
	}
	//: both stores carry the sweep capability as a SIBLING, reached by type
	//: assertion — the published Store method set stays five wide (ADR 0039).
	if _, ok := store.(session.Sweeper); !ok {
		t.Error("the file store does not implement Sweeper")
	}
}

// TestASessionNeverPrintsItself is the consumer-facing half of the redaction
// guarantee: a %v on a session or an identifier, anywhere in a caller's log
// pipeline, cannot leak the bearer secret or the principal.
func TestASessionNeverPrintsItself(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, err := session.NewMemoryStore(session.Config{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	fresh, err := store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bound, err := store.Regenerate(ctx, fresh.ID(), "alice@example.test")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	rendered := renderEveryWay(bound) + renderEveryWay(bound.ID())
	for _, secret := range []string{bound.ID().Reveal(), "alice@example.test"} {
		if contains(rendered, secret) {
			t.Errorf("%q reached a formatted string", secret)
		}
	}
}

// TestAFrameworkCanBuildItsOwnStore pins the two constructors that exist only
// so [session.Store] can be implemented outside the SDK — over Redis, over
// Postgres, over whatever the deployment already runs. Without them the port
// would be published and unimplementable, since every field of [session.Session]
// is unexported.
func TestAFrameworkCanBuildItsOwnStore(t *testing.T) {
	t.Parallel()
	raw := make([]byte, session.IDLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	id, err := session.NewID(raw)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	now := time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)
	built, err := session.NewSession(session.State{
		ID: id, Subject: "user-42",
		CreatedAt: now, LastSeen: now,
		AbsoluteExpiry: now.Add(12 * time.Hour),
		//: deliberately past the ceiling: NewSession clamps, so the "earlier
		//: deadline wins" rule holds for a store the SDK never wrote.
		IdleExpiry: now.Add(99 * time.Hour),
		Data:       map[string]string{"locale": "fr"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if !built.ExpiresAt().Equal(now.Add(12 * time.Hour)) {
		t.Errorf("ExpiresAt = %v, want the ceiling", built.ExpiresAt())
	}
	if value, _ := built.Get("locale"); value != "fr" {
		t.Errorf("locale = %q, want fr", value)
	}
	//: and an unnamed session is refused at construction, as everywhere else.
	if _, err = session.NewSession(session.State{Subject: "nobody"}); !errs.HasReason(err, "INVALID_ID") {
		t.Errorf("NewSession(no ID) = %v, want INVALID_ID", err)
	}
	if _, err = session.NewID(raw[:8]); !errs.HasReason(err, "INVALID_ID") {
		t.Errorf("NewID(8 bytes) = %v, want INVALID_ID", err)
	}
}

// mustCode reads a sentinel's code through the public accessor, which is the
// only route a consumer has.
func mustCode(t *testing.T, sentinel error) errs.Code {
	t.Helper()
	code, ok := errs.CodeOf(sentinel)
	if !ok {
		t.Fatalf("sentinel %v carries no typed code", sentinel)
	}
	return code
}

// testKey builds the AEAD key the sealing tests use.
func testKey(t *testing.T) crypto.Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i * 5)
	}
	key, err := crypto.NewKey(raw)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// renderEveryWay formats value with every verb a log line or a struct dump
// could reach for. String alone does not cover %#v, which is why the list is
// explicit rather than a single %v.
func renderEveryWay(value any) string {
	return fmt.Sprintf("%v|%s|%#v|%+v|%q", value, value, value, value, value)
}

// contains reports whether haystack holds needle.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
