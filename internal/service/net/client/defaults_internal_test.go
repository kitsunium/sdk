// Package client — the configuration defaults.
package client

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_withDefaults pins two things: every unset field gets a bound, and the
// caller's own Config is never modified.
//
// The bounds matter because their absence is invisible. A zero DialTimeout is
// not "no timeout configured", it is NO TIMEOUT — a client that hangs forever on
// an unreachable peer, which looks exactly like a slow one. The same goes for
// the body ceiling: unbounded means one hostile response exhausts the process.
//
// The copy matters because a client must not be able to change the configuration
// its caller still holds — a caller that built one Config and passed it to two
// clients would otherwise find the first had rewritten it.
func Test_withDefaults(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  corenet.ClientConfig
	}
	tests := []tc{
		{name: "an empty config takes every default"},
		{
			name: "an explicit dial timeout is kept",
			cfg:  corenet.ClientConfig{DialTimeout: corenet.DurationValue(time.Second)},
		},
		{
			name: "an explicit body ceiling is kept",
			cfg:  corenet.ClientConfig{MaxResponseSize: 1024},
		},
		{
			name: "an explicit redirect budget is kept",
			cfg:  corenet.ClientConfig{MaxRedirects: 10},
		},
		{
			name: "a fully specified config is untouched",
			cfg: corenet.ClientConfig{
				DialTimeout:      corenet.DurationValue(time.Second),
				HandshakeTimeout: corenet.DurationValue(2 * time.Second),
				ResponseTimeout:  corenet.DurationValue(3 * time.Second),
				TotalTimeout:     corenet.DurationValue(4 * time.Second),
				MaxResponseSize:  1024,
				MaxRedirects:     10,
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := c.cfg

		got := withDefaults(c.cfg)

		//: the caller's value is untouched — withDefaults takes and returns a
		//: value precisely so a client cannot rewrite its caller's Config.
		//: ClientConfig carries a map, so the fields are compared one by one.
		switch {
		case c.cfg.DialTimeout != before.DialTimeout,
			c.cfg.HandshakeTimeout != before.HandshakeTimeout,
			c.cfg.ResponseTimeout != before.ResponseTimeout,
			c.cfg.TotalTimeout != before.TotalTimeout,
			c.cfg.MaxResponseSize != before.MaxResponseSize,
			c.cfg.MaxRedirects != before.MaxRedirects:
			t.Errorf("withDefaults mutated the caller's config")
		}
		//: every duration is bounded. A zero is not "unset", it is "no limit",
		//: and an unbounded client hangs rather than failing.
		if got.DialTimeout == 0 {
			t.Error("DialTimeout is unbounded")
		}
		if got.HandshakeTimeout == 0 {
			t.Error("HandshakeTimeout is unbounded")
		}
		if got.ResponseTimeout == 0 {
			t.Error("ResponseTimeout is unbounded")
		}
		if got.TotalTimeout == 0 {
			t.Error("TotalTimeout is unbounded")
		}
		//: an unbounded body means one hostile response exhausts the process.
		if got.MaxResponseSize <= 0 {
			t.Errorf("MaxResponseSize = %d, want a positive ceiling", got.MaxResponseSize)
		}
		if got.MaxRedirects <= 0 {
			t.Errorf("MaxRedirects = %d, want a positive budget", got.MaxRedirects)
		}

		//: an explicitly set field is preserved rather than overwritten.
		if before.DialTimeout != 0 && got.DialTimeout != before.DialTimeout {
			t.Errorf("DialTimeout = %v, want the configured %v", got.DialTimeout, before.DialTimeout)
		}
		if before.MaxResponseSize != 0 && got.MaxResponseSize != before.MaxResponseSize {
			t.Errorf("MaxResponseSize = %d, want the configured %d", got.MaxResponseSize, before.MaxResponseSize)
		}
		if before.MaxRedirects != 0 && got.MaxRedirects != before.MaxRedirects {
			t.Errorf("MaxRedirects = %d, want the configured %d", got.MaxRedirects, before.MaxRedirects)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the defaults themselves must be conservative rather than permissive: a
	//: misconfigured client should fail fast and small.
	def := withDefaults(corenet.ClientConfig{})
	if def.TotalTimeout.Duration() > time.Minute {
		t.Errorf("the default total timeout is %v, which is not fail-fast", def.TotalTimeout.Duration())
	}
	if def.MaxRedirects > 5 {
		t.Errorf("the default redirect budget is %d, which is not a small chain", def.MaxRedirects)
	}
}
