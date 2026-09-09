package cache_test

import (
	"context"
	"testing"
	"time"

	corecache "github.com/kitsunium/sdk/internal/core/cache"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// threeMethodDouble is a hand-written Store implementation carrying EXACTLY
// the three methods the port declares, and nothing else. It exists to fail
// compilation the day someone adds a fourth method to Store.
type threeMethodDouble struct{}

func (threeMethodDouble) Fetch(context.Context, string) (int, bool, error) { return 0, false, nil }

func (threeMethodDouble) Set(context.Context, string, corecache.EntryValue[int]) error { return nil }

func (threeMethodDouble) Delete(context.Context, string) error { return nil }

// TestThreeMethodDoubleStillSatisfiesStore is the ADR 0039 guard, executable
// rather than documentary.
//
// pkg/v1/cache aliases Store, and Go interfaces are structural: any downstream
// type with these three methods satisfies it today without importing anything
// or declaring intent. Adding a fourth method breaks every one of them at
// compile time with no deprecation window. A contributor who "tidies up" by
// folding EntryFetcher, Tagger or Loader back into Store fails this named test
// before they reach review — which a comment saying "do not" would not have
// achieved.
func TestThreeMethodDoubleStillSatisfiesStore(t *testing.T) {
	t.Parallel()
	store := corecache.Store[int](threeMethodDouble{})
	if _, found, err := store.Fetch(t.Context(), "k"); found || err != nil {
		t.Fatalf("the double answered (%t, %v), want (false, nil)", found, err)
	}
}

// TestNoExpiryIsDistinctFromTheZeroTTL pins the three-way TTL vocabulary. If
// NoExpiry ever became 0, "never expire" and "use the store default" would
// collapse into one spelling and the first would become unsayable.
func TestNoExpiryIsDistinctFromTheZeroTTL(t *testing.T) {
	t.Parallel()
	if corecache.NoExpiry == 0 {
		t.Fatal("NoExpiry is 0 — it would be indistinguishable from 'use the store default'")
	}
	if corecache.NoExpiry > 0 {
		t.Fatalf("NoExpiry is %v — a positive value would be read as a lifetime", corecache.NoExpiry)
	}
}

func TestSentinelsCarryTheirDocumentedCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		code     errs.Code
		reason   string
		exitCode int
	}{
		{"misconfigured", corecache.CacheMisconfigured, corecache.CodeCacheMisconfigured, "CACHE_MISCONFIGURED", 78},
		{"backend", corecache.CacheBackendFailed, corecache.CodeCacheBackendFailed, "CACHE_BACKEND_FAILED", 70},
		{"fill", corecache.CacheFillFailed, corecache.CodeCacheFillFailed, "CACHE_FILL_FAILED", 70},
		{"rejected", corecache.CacheEntryRejected, corecache.CodeCacheEntryRejected, "CACHE_ENTRY_REJECTED", 65},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, ok := errs.CodeOf(tc.err)
			if !ok || code != tc.code {
				t.Fatalf("CodeOf = (%v, %t), want (%v, true)", code, ok, tc.code)
			}
			if !errs.HasReason(tc.err, tc.reason) {
				t.Fatalf("reason is not %q", tc.reason)
			}
			if got := errs.ExitCodeOf(tc.err); got != tc.exitCode {
				t.Fatalf("ExitCodeOf = %d, want %d", got, tc.exitCode)
			}
		})
	}
}

// TestEveryCodeSitsInTheAllocatedRange guards the ADR 0049 block: 0.2.18.* is
// this package's slot, and a constant drifting outside it would collide with
// whoever owns the neighbouring range.
func TestEveryCodeSitsInTheAllocatedRange(t *testing.T) {
	t.Parallel()
	const wantPrefix uint32 = 0x00_02_12_00
	codes := []errs.Code{
		corecache.CodeCacheMisconfigured,
		corecache.CodeCacheBackendFailed,
		corecache.CodeCacheFillFailed,
		corecache.CodeCacheEntryRejected,
	}
	for _, code := range codes {
		if uint32(code)&0xFF_FF_FF_00 != wantPrefix {
			t.Fatalf("code %v is outside the 0.2.18.* block", code)
		}
	}
}

// TestFillIsAFuncPort is the structural half of ADR 0039: a func type cannot
// grow a method, so Fill can never break a downstream implementer the way an
// interface could. The assignment below is the whole assertion.
func TestFillIsAFuncPort(t *testing.T) {
	t.Parallel()
	fill := corecache.Fill[string](func(context.Context) (corecache.EntryValue[string], error) {
		return corecache.EntryValue[string]{Value: "v", TTL: time.Minute}, nil
	})
	entry, err := fill(t.Context())
	if err != nil || entry.Value != "v" {
		t.Fatalf("fill = (%+v, %v), want the entry it returns", entry, err)
	}
}
