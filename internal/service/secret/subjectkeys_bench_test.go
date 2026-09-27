package secret_test

import (
	"fmt"
	"testing"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// benchPlaintext is the value every benchmark seals: the size the SDK's
// crypto figures are quoted at, about an e-mail address, a name and a street.
var benchPlaintext = []byte("jane.doe@example.org / Jane Doe / 12 rue de la Paix, 75002 Paris")

// benchEngine is a deployment with one root version and an engine over it.
func benchEngine(b *testing.B, cacheSize int) (*subjectFixture, *svcsecret.SubjectKeys) {
	b.Helper()
	f := newSubjectFixture(b)
	cfg := svcsecret.SubjectKeysConfig{Root: f.root, Store: f.store, CacheSize: cacheSize, Clock: f.clk}
	if cacheSize > 0 {
		cfg.CacheTTL = time.Hour
	}
	keys, err := svcsecret.NewSubjectKeys(cfg)
	if err != nil {
		b.Fatalf("NewSubjectKeys: %v", err)
	}
	return f, keys
}

// BenchmarkSubjectKeysSeal is the cost of sealing one 64-byte value under a
// subject's key: with the key cached (the steady state of a hot subject), and
// with no cache, where every call reads the store and unwraps under the root.
func BenchmarkSubjectKeysSeal(b *testing.B) {
	for _, size := range []int{1024, 0} {
		b.Run(fmt.Sprintf("cache=%d", size), func(b *testing.B) {
			_, keys := benchEngine(b, size)
			ctx := b.Context()
			if _, err := keys.Seal(ctx, "user:1", benchPlaintext, "reports", "r-1", "/email"); err != nil {
				b.Fatalf("Seal: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := keys.Seal(ctx, "user:1", benchPlaintext, "reports", "r-1", "/email"); err != nil {
					b.Fatalf("Seal: %v", err)
				}
			}
		})
	}
}

// BenchmarkSubjectKeysOpen is the cost of opening one 64-byte value, cached
// and uncached — the uncached figure is what a subject not read for CacheTTL
// pays once.
func BenchmarkSubjectKeysOpen(b *testing.B) {
	for _, size := range []int{1024, 0} {
		b.Run(fmt.Sprintf("cache=%d", size), func(b *testing.B) {
			_, keys := benchEngine(b, size)
			ctx := b.Context()
			box, err := keys.Seal(ctx, "user:1", benchPlaintext, "reports", "r-1", "/email")
			if err != nil {
				b.Fatalf("Seal: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := keys.Open(ctx, box, "reports", "r-1", "/email"); err != nil {
					b.Fatalf("Open: %v", err)
				}
			}
		})
	}
}

// BenchmarkCryptoSealBaseline is the AES-256-GCM seal underneath, alone: the
// figure the difference with BenchmarkSubjectKeysSeal/cache=1024 is the price
// of the subject key over a bare key.
func BenchmarkCryptoSealBaseline(b *testing.B) {
	key, err := corecrypto.NewKey(make([]byte, corecrypto.KeyLen))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := corecrypto.Seal("aes-256-gcm", key, benchPlaintext, []byte("reports/r-1/email")); err != nil {
			b.Fatalf("Seal: %v", err)
		}
	}
}

// BenchmarkSubjectKeysRewrap is what a rotation of the root costs per
// subject: one unwrap, one wrap and one compare-and-swap for each key, over
// stores of growing size. ns/key is the figure to multiply by a population.
func BenchmarkSubjectKeysRewrap(b *testing.B) {
	for _, subjects := range []int{100, 10_000} {
		b.Run(fmt.Sprintf("subjects=%d", subjects), func(b *testing.B) {
			f, keys := benchEngine(b, 0)
			ctx := b.Context()
			for index := range subjects {
				if _, err := keys.Seal(ctx, fmt.Sprintf("user:%d", index), benchPlaintext); err != nil {
					b.Fatalf("Seal: %v", err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				//: a rotation: a new root version, and the root pruned to a
				//: Keep of 2, so every pass moves every key from the version
				//: before to the newest — as a rotation every 30 days does.
				b.StopTimer()
				putRandomVersion(b, f.roots, rootName)
				if err := f.roots.Prune(ctx, rootName, 2); err != nil {
					b.Fatalf("Prune: %v", err)
				}
				b.StartTimer()
				report, err := keys.Rewrap(ctx)
				if err != nil || report.Rewrapped != subjects {
					b.Fatalf("Rewrap = (%+v, %v)", report, err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*subjects), "ns/key")
		})
	}
}

// BenchmarkSubjectKeysOldestRoot is what the rotator's InUse question costs:
// a header read per key, no cryptography.
func BenchmarkSubjectKeysOldestRoot(b *testing.B) {
	const subjects int = 10_000
	_, keys := benchEngine(b, 0)
	ctx := b.Context()
	for index := range subjects {
		if _, err := keys.Seal(ctx, fmt.Sprintf("user:%d", index), benchPlaintext); err != nil {
			b.Fatalf("Seal: %v", err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if oldest, err := keys.OldestRoot(ctx); err != nil || oldest != 1 {
			b.Fatalf("OldestRoot = (%d, %v)", oldest, err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*subjects), "ns/key")
}
