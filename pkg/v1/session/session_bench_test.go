package session_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/session"
)

// sinks so nothing measured below can be proven unused and elided.
var (
	sessionSink session.Session
	strSink     string
	idSink      session.ID
	errSink     error
)

// benchConfig is the policy every store below shares: a realistic idle window
// and absolute cap, so the sliding-expiry arithmetic Load performs is actually
// exercised rather than short-circuited by a zero.
func benchConfig() session.Config {
	return session.Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour}
}

// benchMemoryStore builds the store the other benchmarks reuse.
func benchMemoryStore(b *testing.B) session.Store {
	b.Helper()
	store, err := session.NewMemoryStore(benchConfig())
	if err != nil {
		b.Fatalf("NewMemoryStore: %v", err)
	}
	return store
}

// benchFileStore builds a file store rooted in the test's own directory.
func benchFileStore(b *testing.B) session.Store {
	b.Helper()
	key, err := crypto.NewKey(make([]byte, crypto.KeyLen))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	//: b.TempDir() alone is not enough here: this container's TMPDIR carries a
	//: POSIX ACL that leaves new directories at 0775, and the store REFUSES a
	//: location that is not private — correctly, since a world-readable
	//: directory of sealed sessions is a directory of sealed sessions anybody
	//: can copy. The benchmark therefore makes its own 0700 directory instead
	//: of skipping, so the durable path is actually measured.
	dir := filepath.Join(b.TempDir(), "sessions")
	if mkErr := os.Mkdir(dir, 0o700); mkErr != nil {
		b.Fatalf("Mkdir: %v", mkErr)
	}
	//: Mkdir's mode is masked by umask, so the permissions are set explicitly.
	if chErr := os.Chmod(dir, 0o700); chErr != nil {
		b.Fatalf("Chmod: %v", chErr)
	}
	store, err := session.NewFileStore(session.FileConfig{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 12 * time.Hour,
		Dir: dir, Key: key,
	})
	if err != nil {
		b.Skipf("NewFileStore unavailable on this platform: %v", err)
	}
	return store
}

// BenchmarkMemory_New is the anonymous-session mint: a 256-bit identifier from
// the CSPRNG plus a map insert. It runs once per first-contact request.
func BenchmarkMemory_New(b *testing.B) {
	store := benchMemoryStore(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionSink, errSink = store.New(ctx)
	}
}

// BenchmarkMemory_Load is the number that matters most: it runs on EVERY
// request that carries a session cookie. It is deliberately not a read — Load
// slides the idle window, so it takes the write lock, which is exactly the
// design ADR 0045 argues for and the reason this is benchmarked rather than
// assumed cheap.
func BenchmarkMemory_Load(b *testing.B) {
	store := benchMemoryStore(b)
	ctx := b.Context()
	seed, err := store.New(ctx)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionSink, errSink = store.Load(ctx, seed.ID())
	}
}

// BenchmarkMemory_Save is the write half, paid whenever a handler changed
// something on the session.
func BenchmarkMemory_Save(b *testing.B) {
	store := benchMemoryStore(b)
	ctx := b.Context()
	seed, err := store.New(ctx)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = store.Save(ctx, seed)
	}
}

// BenchmarkMemory_Regenerate is the login path — the ONLY call that binds a
// subject, and the reason session fixation has no spelling in this API. It
// mints a new identifier and retires the old one, so it is more expensive than
// New, and it runs once per login rather than once per request.
func BenchmarkMemory_Regenerate(b *testing.B) {
	store := benchMemoryStore(b)
	ctx := b.Context()
	current, err := store.New(ctx)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		next, regenErr := store.Regenerate(ctx, current.ID(), "user-42")
		if regenErr != nil {
			b.Fatalf("Regenerate: %v", regenErr)
		}
		//: chain onto the identifier just minted; the previous one is retired.
		current = next
	}
	sessionSink = current
}

// BenchmarkMemory_LoadParallel is the contended read, which is what a real
// server does: N request goroutines resolving sessions at once, against a store
// whose Load takes the write lock.
func BenchmarkMemory_LoadParallel(b *testing.B) {
	store := benchMemoryStore(b)
	ctx := b.Context()
	seed, err := store.New(ctx)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	id := seed.ID()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, loadErr := store.Load(ctx, id); loadErr != nil {
				b.Errorf("Load: %v", loadErr)
				return
			}
		}
	})
}

// BenchmarkFile_New and BenchmarkFile_Load are the durable store. The ratio
// against the memory rows is what persistence costs per request, and it is the
// fact a caller sizes a deployment on.
func BenchmarkFile_New(b *testing.B) {
	store := benchFileStore(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionSink, errSink = store.New(ctx)
	}
}

func BenchmarkFile_Load(b *testing.B) {
	store := benchFileStore(b)
	ctx := b.Context()
	seed, err := store.New(ctx)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sessionSink, errSink = store.Load(ctx, seed.ID())
	}
}

// BenchmarkSealer_Seal and _Open are the cookie edge: an AEAD seal on the way
// out and an open on the way in, once per request each. An Open that failed
// must also be cheap, which the tampered row checks.
func BenchmarkSealer_Seal(b *testing.B) {
	store := benchMemoryStore(b)
	sealer := benchSealer(b)
	seed, err := store.New(b.Context())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	id := seed.ID()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = sealer.Seal(id)
	}
}

func BenchmarkSealer_Open(b *testing.B) {
	store := benchMemoryStore(b)
	sealer := benchSealer(b)
	seed, err := store.New(b.Context())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	sealed, err := sealer.Seal(seed.ID())
	if err != nil {
		b.Fatalf("Seal: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		idSink, errSink = sealer.Open(sealed)
	}
}

func BenchmarkSealer_OpenTampered(b *testing.B) {
	store := benchMemoryStore(b)
	sealer := benchSealer(b)
	seed, err := store.New(b.Context())
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	sealed, err := sealer.Seal(seed.ID())
	if err != nil {
		b.Fatalf("Seal: %v", err)
	}
	//: flip the last byte — the cheapest tamper the AEAD must still reject.
	tampered := sealed[:len(sealed)-1] + "A"
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		idSink, errSink = sealer.Open(tampered)
	}
	if errSink == nil {
		b.Fatal("a tampered cookie opened")
	}
}

// benchSealer builds the AEAD cookie sealer the three rows above share.
func benchSealer(b *testing.B) session.Sealer {
	b.Helper()
	key, err := crypto.NewKey(make([]byte, crypto.KeyLen))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	sealer, err := session.NewSealer(key, "bench")
	if err != nil {
		b.Fatalf("NewSealer: %v", err)
	}
	return sealer
}
