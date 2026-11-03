package sample_test

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coresample "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/sample"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/sample"
)

// messageCounter counts the forwarded records per message.
type messageCounter struct {
	mu   sync.Mutex
	seen map[string]int
}

func newMessageCounter() *messageCounter { return &messageCounter{seen: map[string]int{}} }

func (c *messageCounter) Write(_ context.Context, r corelogger.RecordEvent, p []byte) (int, error) {
	c.mu.Lock()
	c.seen[r.Message]++
	c.mu.Unlock()
	return len(p), nil
}
func (*messageCounter) Flush(context.Context) error { return nil }
func (*messageCounter) Close() error                { return nil }

func (c *messageCounter) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[msg]
}

// write sends one Info record of msg.
func write(t *testing.T, s corelogger.Sink, lv level.Level, msg string) {
	t.Helper()
	if _, err := s.Write(context.Background(), corelogger.RecordEvent{Level: lv, Message: msg}, []byte(msg)); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// TestKeyedSamplesPerMessageAndWindow (#1017): 100 writes of A and 1 of B in
// one window forward 2 + 9 of A and B; an Error is forwarded whatever its
// key's state; Dropped and OnDrop count every drop; advancing the clock by the
// window re-opens it.
func TestKeyedSamplesPerMessageAndWindow(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_000, 0))
	down := newMessageCounter()
	var onDrop atomic.Int64
	s, err := sample.NewKeyed(down, &sample.KeyedConfig{
		Window: time.Second, First: 2, Thereafter: 10, Clock: clk,
		OnDrop: func(corelogger.RecordEvent) { onDrop.Add(1) },
	})
	if err != nil {
		t.Fatalf("NewKeyed: %v", err)
	}
	for i := range 100 {
		write(t, s, level.Info, "A")
		if i == 50 {
			write(t, s, level.Info, "B")
		}
	}
	for range 5 {
		write(t, s, level.Error, "A")
	}
	if a, b := down.count("A"), down.count("B"); a != 2+9+5 || b != 1 {
		t.Fatalf("forwarded A=%d B=%d, want A=%d (2 + 9 + 5 errors) and B=1", a, b, 2+9+5)
	}
	if s.Dropped() != 89 || onDrop.Load() != 89 {
		t.Fatalf("Dropped() = %d, OnDrop fired %d times, want 89 and 89", s.Dropped(), onDrop.Load())
	}
	clk.Advance(time.Second)
	write(t, s, level.Info, "A")
	write(t, s, level.Info, "A")
	write(t, s, level.Info, "A")
	if a := down.count("A"); a != 16+2 {
		t.Fatalf("after the window re-opened A=%d, want %d", a, 18)
	}
}

// TestKeyedTableNeverPassesMaxKeys (#1017): 10 000 distinct keys never grow
// the table past MaxKeys, and every write past the bound is forwarded or
// dropped by the overflow counter — counted, never lost.
func TestKeyedTableNeverPassesMaxKeys(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_000, 0))
	down := newMessageCounter()
	s, err := sample.NewKeyed(down, &sample.KeyedConfig{MaxKeys: 16, First: 1, Thereafter: 1000, Clock: clk})
	if err != nil {
		t.Fatalf("NewKeyed: %v", err)
	}
	const n = 10_000
	for i := range n {
		write(t, s, level.Debug, "id="+strconv.Itoa(i))
	}
	forwarded := 0
	down.mu.Lock()
	for _, c := range down.seen {
		forwarded += c
	}
	down.mu.Unlock()
	if forwarded+int(s.Dropped()) != n {
		t.Fatalf("forwarded %d + dropped %d != %d writes", forwarded, s.Dropped(), n)
	}
	//: 16 keys in the table forward their first record; the overflow counter
	//: forwards its first and its 1001st, 2001st…
	if forwarded > 16+1+n/1000 {
		t.Fatalf("forwarded %d: the table grew past MaxKeys", forwarded)
	}
}

// TestKeyedZeroConfigIsTheDocumentedSampler and the refusals: the zero
// KeyedConfig forwards 100 of a key then 1 in 100; negative bounds and a nil
// downstream are refused.
func TestKeyedZeroConfigIsTheDocumentedSampler(t *testing.T) {
	t.Parallel()
	down := newMessageCounter()
	s, err := sample.NewKeyed(down, &sample.KeyedConfig{Clock: clock.NewManualClock(time.Unix(1, 0))})
	if err != nil {
		t.Fatalf("NewKeyed: %v", err)
	}
	for range 1000 {
		write(t, s, level.Info, "same")
	}
	if got := down.count("same"); got != 100+9 {
		t.Fatalf("zero config forwarded %d of 1000, want 109", got)
	}
	for _, cfg := range []sample.KeyedConfig{{Window: -1}, {First: -1}, {Thereafter: -1}, {MaxKeys: -1}} {
		if _, err := sample.NewKeyed(down, &cfg); !errs.HasCode(err, coresample.CodeSampleRateInvalid) {
			t.Errorf("NewKeyed(%+v) = %v, want RateInvalid", cfg, err)
		}
	}
	if _, err := sample.NewKeyed(nil, nil); !errs.HasCode(err, coresample.CodeSampleDownstreamNil) {
		t.Errorf("NewKeyed(nil) = %v, want DownstreamNil", err)
	}
}

// TestKeyedCustomKeyAndBypass: a caller's Key groups records across messages,
// and a bypass floor of Error samples warnings too.
func TestKeyedCustomKeyAndBypass(t *testing.T) {
	t.Parallel()
	down := newMessageCounter()
	s, err := sample.NewKeyed(down, &sample.KeyedConfig{
		First: 1, Thereafter: 1000, Clock: clock.NewManualClock(time.Unix(1, 0)),
		Key:            func(corelogger.RecordEvent) string { return "one" },
		MinLevelBypass: level.Fixed(level.Error),
	})
	if err != nil {
		t.Fatalf("NewKeyed: %v", err)
	}
	write(t, s, level.Warn, "x")
	write(t, s, level.Warn, "y")
	write(t, s, level.Error, "z")
	if down.count("x") != 1 || down.count("y") != 0 || down.count("z") != 1 {
		t.Fatalf("seen %v", down.seen)
	}
}
