package sample

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coresample "github.com/kitsunium/sdk/internal/core/observe/logger/middleware/sample"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

const (
	// DefaultWindow is the keyed sampler's window when KeyedConfig.Window is zero.
	DefaultWindow time.Duration = time.Second
	// DefaultFirst is how many records of one key a window forwards before the
	// keyed sampler thins them, when KeyedConfig.First is zero.
	DefaultFirst int = 100
	// DefaultThereafter is the keyed sampler's 1-in-N past First, when
	// KeyedConfig.Thereafter is zero.
	DefaultThereafter int = 100
	// DefaultMaxKeys bounds the keyed sampler's table when KeyedConfig.MaxKeys is
	// zero.
	DefaultMaxKeys int = 1024
)

// KeyedConfig tunes NewKeyed. Every field is optional: the zero value is
// the documented sampler (one second, the first 100 records of a key, then
// 1 in 100, a table of 1024 keys, Warn and above never sampled).
type KeyedConfig struct {
	// Window is the period a key's counts are kept over; zero is DefaultWindow.
	Window time.Duration
	// First is how many records of a key a window forwards; zero is DefaultFirst.
	First int
	// Thereafter forwards every Thereafter-th record of a key past First in a
	// window; zero is DefaultThereafter, one forwards them all.
	Thereafter int
	// MaxKeys bounds the table of keys; zero is DefaultMaxKeys. A new key
	// past the bound, once the keys whose window ended are swept, is
	// counted under one shared overflow counter.
	MaxKeys int
	// Clock measures the windows; nil is clock.System.
	Clock clock.Clock
	// Key maps a record to its sampling key; nil keys on the level and the
	// message. It is called on every Write and must not retain the record.
	Key func(r corelogger.RecordEvent) string
	// MinLevelBypass is the floor at and above which a record is forwarded
	// without being counted, read on every Write; nil is level.Warn. Pass
	// level.Fixed(level.Error) to sample warnings too.
	MinLevelBypass level.Leveler
	// OnDrop, when non-nil, is called with every record the sampler drops,
	// on the Write that drops it.
	OnDrop func(r corelogger.RecordEvent)
}

// keyedKey is a table key: the level and the message by default, the
// caller's Key with a zero level otherwise. A struct, so the default key is
// built without concatenating a string per record.
type keyedKey struct {
	// lv is the record's level (default key) or zero (caller's Key).
	lv level.Level
	// s is the record's message (default key) or the caller's key.
	s string
}

// keyedCounter is one key's count in its current window.
type keyedCounter struct {
	// start is when the counter's window began.
	start time.Time
	// count is how many records of the key the window has seen.
	count int
}

// Keyed is the keyed, windowed sampler NewKeyed builds: a corelogger.Sink
// that forwards the first records of each key in a window and then one in
// N, and counts what it drops. Safe for concurrent use.
type Keyed struct {
	// downstream receives the forwarded records.
	downstream corelogger.Sink
	// cfg is the resolved configuration, every default applied.
	cfg KeyedConfig
	// mu guards keys, overflow and swept.
	mu sync.Mutex
	// keys is the per-key table, at most cfg.MaxKeys entries; a counter is
	// updated in place, so a known key costs one hash per record.
	keys map[keyedKey]*keyedCounter
	// overflow counts every key the full table could not admit.
	overflow keyedCounter
	// swept is when the table was last swept of ended windows, so a full
	// table is swept at most once a window.
	swept time.Time
	// dropped counts every dropped record.
	dropped atomic.Uint64
}

// NewKeyed constructs the keyed, windowed sampler over downstream. A nil cfg
// is the zero KeyedConfig, and a zero field of cfg takes its documented
// default (DefaultWindow, DefaultFirst,
// DefaultThereafter, DefaultMaxKeys, clock.System, the level-and-message
// key, a Warn bypass), so the zero KeyedConfig is the documented sampler
// and never an inert one; a negative Window, First, Thereafter or MaxKeys
// is refused with RateInvalid, a nil downstream with DownstreamNil.
func NewKeyed(downstream corelogger.Sink, cfg *KeyedConfig) (sink *Keyed, err error) {
	var resolved KeyedConfig
	//: a nil config is the zero one; the caller's is copied, never kept.
	if cfg != nil {
		resolved = *cfg
	}
	//: a negative bound is a mistake, not a default.
	if resolved.Window < 0 || resolved.First < 0 || resolved.Thereafter < 0 || resolved.MaxKeys < 0 {
		return nil, coresample.RateInvalid
	}
	//: kept records need somewhere to land.
	if downstream == nil {
		return nil, coresample.DownstreamNil
	}
	resolveKeyed(&resolved)
	//: the table is sized for its bound, which it never passes.
	return &Keyed{downstream: downstream, cfg: resolved, keys: make(map[keyedKey]*keyedCounter, resolved.MaxKeys)}, nil
}

// resolveKeyed applies every zero field's documented default.
func resolveKeyed(cfg *KeyedConfig) {
	cfg.Window = orDefault(cfg.Window, DefaultWindow)
	cfg.First = orDefault(cfg.First, DefaultFirst)
	cfg.Thereafter = orDefault(cfg.Thereafter, DefaultThereafter)
	cfg.MaxKeys = orDefault(cfg.MaxKeys, DefaultMaxKeys)
	//: the real clock is a description, not a guess.
	if cfg.Clock == nil {
		cfg.Clock = clock.System
	}
	//: warnings and errors are never sampled away by default.
	if cfg.MinLevelBypass == nil {
		cfg.MinLevelBypass = level.Fixed(level.Warn)
	}
}

// orDefault is v, or def when v is zero.
func orDefault[T int | time.Duration](v, def T) T {
	//: zero is the documented default (ADR 0031).
	if v == 0 {
		return def
	}
	return v
}

// Write forwards r when its level bypasses sampling, or when its key's
// count in the current window is within First or lands on the
// Thereafter-th; otherwise it drops it, counts it, calls OnDrop and
// returns (0, nil), as New's dropped writes do.
func (s *Keyed) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: a warning or an error is never sampled away; below it the key's count
	//: decides.
	if r.Level >= s.cfg.MinLevelBypass.Level() || s.keep(s.keyOf(r)) {
		return s.downstream.Write(ctx, r, p)
	}
	s.dropped.Add(1)
	//: every drop is reported, so sampling is told apart from silence.
	if s.cfg.OnDrop != nil {
		s.cfg.OnDrop(r)
	}
	//: the documented contract of a dropped write.
	return 0, nil
}

// keyOf is r's table key: the caller's Key, or the level and the message.
func (s *Keyed) keyOf(r corelogger.RecordEvent) keyedKey {
	//: the caller's key, level-free.
	if s.cfg.Key != nil {
		return keyedKey{s: s.cfg.Key(r)}
	}
	//: the default key.
	return keyedKey{lv: r.Level, s: r.Message}
}

// keep counts one record of key in its window and reports whether it is
// forwarded: within First, or on every Thereafter-th past it.
func (s *Keyed) keep(key keyedKey) bool {
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.keys[key]
	//: a new key enters the table, or the overflow counter when it is full.
	if c == nil {
		//: the table is full of live windows: the shared counter.
		if !s.admit(now) {
			c = &s.overflow
		} else {
			c = &keyedCounter{}
			s.keys[key] = c
		}
	}
	s.advance(c, now)
	return s.forwards(c.count)
}

// admit reports whether the table has room for one more key, sweeping the
// keys whose window ended — at most once a window — when it is full. The
// caller holds s.mu.
func (s *Keyed) admit(now time.Time) bool {
	//: room left.
	if len(s.keys) < s.cfg.MaxKeys {
		return true
	}
	//: a sweep this window already found nothing to free.
	if now.Sub(s.swept) < s.cfg.Window {
		return false
	}
	s.swept = now
	//: a key whose window ended restarts at zero anyway: forget it.
	for k, c := range s.keys {
		//: still counting.
		if now.Sub(c.start) < s.cfg.Window {
			continue
		}
		delete(s.keys, k)
	}
	return len(s.keys) < s.cfg.MaxKeys
}

// advance counts one record into c, restarting c's window when it ended.
func (s *Keyed) advance(c *keyedCounter, now time.Time) {
	//: the window ended (or never began): a new one starts with this record.
	if c.count == 0 || now.Sub(c.start) >= s.cfg.Window {
		c.start, c.count = now, 1
		return
	}
	c.count++
}

// forwards reports whether the count-th record of a window is forwarded.
func (s *Keyed) forwards(count int) bool {
	//: the first First, then every Thereafter-th.
	return count <= s.cfg.First || (count-s.cfg.First)%s.cfg.Thereafter == 0
}

// Flush forwards to the downstream sink.
func (s *Keyed) Flush(ctx context.Context) error {
	//: the sampler buffers nothing.
	return s.downstream.Flush(ctx)
}

// Close forwards to the downstream sink.
func (s *Keyed) Close() error {
	//: the sampler owns nothing.
	return s.downstream.Close()
}

// Dropped reports how many records the sampler has dropped since it was built.
func (s *Keyed) Dropped() uint64 {
	//: an atomic read, safe beside concurrent Writes.
	return s.dropped.Load()
}
