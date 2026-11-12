package http1_test

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// subjectVerifier authenticates token "user-<n>" as subject "user-<n>".
type subjectVerifier struct{}

func (subjectVerifier) VerifyBearer(tok []byte, auth *corenet.HTTP1AuthValue) bool {
	return auth.SetSubject(tok)
}

func rateAuthed(sub string) string {
	return "GET /api HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer " + sub + "\r\n\r\n"
}

func newRateChain(t *testing.T, rate float64, burst, keys int, clk clock.Clock, by http1.RateKey, opts ...http1.RateOption) corenet.HTTP1Handler {
	t.Helper()
	buckets, err := http1.NewKeyedBuckets(http1.KeyedBucketsValue{Rate: rate, Burst: burst, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := http1.Routes(http1.RouteValue{ID: 1, Path: "/api"})
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(subjectVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := http1.RateLimit(buckets, by, append(opts, http1.RateClock(clk))...)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	return corenet.Chain[corenet.HTTP1Handler](app, routes, bearer, limit)
}

func serveRate(t *testing.T, h corenet.HTTP1Handler, raw string) *corenet.HTTP1ResponseValue {
	t.Helper()
	var res corenet.HTTP1ResponseValue
	h.ServeHTTP1(parse(t, raw), &res)
	if b, ok := h.(corenet.HTTP1Batcher); ok {
		b.EndBatch(0)
	}
	return &res
}

// TestRateLimit_bySubject is the acceptance: the 11th request of one subject in
// a second is refused, another subject is served.
func TestRateLimit_bySubject(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(1000, 0))
	h := newRateChain(t, 10, 10, 1000, clk, http1.RateKeySubject())
	for i := range 10 {
		if res := serveRate(t, h, rateAuthed("alice")); res.Status != 0 {
			t.Fatalf("request %d: status %d", i, res.Status)
		}
		clk.Advance(time.Millisecond)
	}
	res := serveRate(t, h, rateAuthed("alice"))
	if res.Status != 429 || !res.Close || res.RetryAfter != 1 {
		t.Fatalf("the 11th: status %d close %v retry %d", res.Status, res.Close, res.RetryAfter)
	}
	wire, err := http1.AppendResponse(nil, &corenet.HTTP1RequestValue{Method: []byte("GET")}, res, res.Close)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"429", "Retry-After: 1\r\n", "Connection: close\r\n"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("no %q in %q", want, wire)
		}
	}
	if res := serveRate(t, h, rateAuthed("bob")); res.Status != 0 {
		t.Fatalf("another subject: status %d", res.Status)
	}
	//: a second later the bucket has refilled.
	clk.Advance(time.Second)
	if res := serveRate(t, h, rateAuthed("alice")); res.Status != 0 {
		t.Fatalf("after a second: status %d", res.Status)
	}
}

func TestRateLimit_keys(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(1000, 0))
	byField := newRateChain(t, 1, 1, 100, clk, http1.RateKeyField("X-Api-Key"))
	withKey := func(k string) string {
		return "GET /api HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer u\r\nX-Api-Key: " + k + "\r\n\r\n"
	}
	if serveRate(t, byField, withKey("a")).Status != 0 || serveRate(t, byField, withKey("b")).Status != 0 {
		t.Fatal("two keys, one each, should pass")
	}
	if serveRate(t, byField, withKey("a")).Status != 429 {
		t.Fatal("key a twice should be limited")
	}

	byRoute := newRateChain(t, 1, 1, 100, clk, http1.RateKeyRoute())
	if serveRate(t, byRoute, rateAuthed("u")).Status != 0 || serveRate(t, byRoute, rateAuthed("v")).Status != 429 {
		t.Fatal("one route shares one bucket")
	}

	byLane := newRateChain(t, 1, 1, 100, clk, http1.RateKeyLane())
	if serveRate(t, byLane, rateAuthed("u")).Status != 0 || serveRate(t, byLane, rateAuthed("v")).Status != 429 {
		t.Fatal("one lane shares one bucket")
	}
}

func TestRateLimit_exemptRoute(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(1000, 0))
	h := newRateChain(t, 1, 1, 100, clk, http1.RateKeySubject(), http1.RateExempt(1))
	for range 5 {
		if serveRate(t, h, rateAuthed("alice")).Status != 0 {
			t.Fatal("an exempt route was limited")
		}
	}
}

func TestRateLimit_refusals(t *testing.T) {
	buckets, err := http1.NewKeyedBuckets(http1.KeyedBucketsValue{Rate: 1, Burst: 1, Keys: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		l    corenet.HTTP1Limiter
		key  http1.RateKey
		opts []http1.RateOption
		want string
	}{
		{"nil limiter", nil, http1.RateKeySubject(), nil, "limiter"},
		{"no key", buckets, http1.RateKey{}, nil, "key"},
		{"a field that is not a token", buckets, http1.RateKeyField("a b"), nil, "key"},
		{"an exempt route of zero", buckets, http1.RateKeySubject(), []http1.RateOption{http1.RateExempt(0)}, "exempt"},
		{"a nil clock", buckets, http1.RateKeySubject(), []http1.RateOption{http1.RateClock(nil)}, "clock"},
	} {
		if _, err := http1.RateLimit(c.l, c.key, c.opts...); !misconfiguredAs(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	for _, c := range []struct {
		name string
		cfg  http1.KeyedBucketsValue
		want string
	}{
		{"rate zero", http1.KeyedBucketsValue{Burst: 1, Keys: 1}, "rate"},
		{"rate negative", http1.KeyedBucketsValue{Rate: -1, Burst: 1, Keys: 1}, "rate"},
		{"burst zero", http1.KeyedBucketsValue{Rate: 1, Keys: 1}, "burst"},
		{"keys zero", http1.KeyedBucketsValue{Rate: 1, Burst: 1}, "keys"},
		{"keys negative", http1.KeyedBucketsValue{Rate: 1, Burst: 1, Keys: -1}, "keys"},
	} {
		if _, err := http1.NewKeyedBuckets(c.cfg); !misconfiguredAs(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// TestKeyedBuckets_evictsTheLeastRecent is the acceptance: a table capped at
// 1 000 keys under 2 000 distinct subjects evicts the oldest and does not grow.
func TestKeyedBuckets_evictsTheLeastRecent(t *testing.T) {
	b, err := http1.NewKeyedBuckets(http1.KeyedBucketsValue{Rate: 1, Burst: 1, Keys: 1000})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([][]byte, 2000)
	for i := range keys {
		keys[i] = fmt.Appendf(nil, "user-%d", i)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	now := int64(1)
	for _, k := range keys {
		b.Allow(k, now)
	}
	runtime.ReadMemStats(&after)
	if delta := int64(after.TotalAlloc) - int64(before.TotalAlloc); delta > 64<<10 {
		t.Fatalf("the table allocated %d bytes under 2 000 keys", delta)
	}
	//: the most recent thousand are remembered: empty buckets, refused.
	refused := 0
	for _, k := range keys[1000:] {
		if ok, _ := b.Allow(k, now); !ok {
			refused++
		}
	}
	if refused != 1000 {
		t.Fatalf("%d of the most recent thousand still known", refused)
	}
	//: the oldest were evicted: a full bucket again.
	if ok, _ := b.Allow(keys[0], now); !ok {
		t.Fatal("the least recent key was not forgotten")
	}
}

func TestKeyedBuckets_refillAndRetryAfter(t *testing.T) {
	b, err := http1.NewKeyedBuckets(http1.KeyedBucketsValue{Rate: 0.5, Burst: 2, Keys: 4})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("k")
	now := int64(1e9)
	for i := range 2 {
		if ok, _ := b.Allow(key, now); !ok {
			t.Fatalf("token %d refused", i)
		}
	}
	ok, after := b.Allow(key, now)
	if ok || after != 2 {
		t.Fatalf("the empty bucket: ok %v retry %d, want 2 at half a token a second", ok, after)
	}
	if ok, _ := b.Allow(key, now+int64(2*time.Second)); !ok {
		t.Fatal("two seconds should refill one token")
	}
	//: a clock that goes back never mints tokens.
	if ok, _ := b.Allow(key, now); ok {
		t.Fatal("a backward clock refilled")
	}
}

// TestKeyedBuckets_matchesAnLRUModel drives the table with a random mix of known
// and new keys and compares every decision with a plain LRU: a known key's single
// token is spent, a key fallen out of the table is new again.
func TestKeyedBuckets_matchesAnLRUModel(t *testing.T) {
	for _, capacity := range []int{1, 2, 3, 16, 100} {
		b, err := http1.NewKeyedBuckets(http1.KeyedBucketsValue{Rate: 1e-9, Burst: 1, Keys: capacity})
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewPCG(uint64(capacity), 7))
		var lru []string
		for i := range 20000 {
			k := fmt.Sprintf("k%d", rng.IntN(capacity*3))
			ok, _ := b.Allow([]byte(k), int64(i))
			idx := slices.Index(lru, k)
			want := idx < 0 //: a key the model has not seen is served once.
			if ok != want {
				t.Fatalf("cap %d op %d key %q: Allow=%v, model %v (lru %v)", capacity, i, k, ok, want, lru)
			}
			if idx >= 0 {
				lru = slices.Delete(lru, idx, idx+1)
			}
			lru = slices.Insert(lru, 0, k)
			if len(lru) > capacity {
				lru = lru[:capacity]
			}
		}
	}
}
