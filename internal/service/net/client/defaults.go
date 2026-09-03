// Package client — the configuration defaults.
package client

import (
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Defaults chosen to be safe rather than permissive: a misconfigured client
// should fail fast and small, never hang or buffer without bound.
const (
	// defaultDialTimeout bounds connection establishment.
	defaultDialTimeout time.Duration = 5 * time.Second
	// defaultHandshakeTimeout bounds the TLS handshake.
	defaultHandshakeTimeout time.Duration = 5 * time.Second
	// defaultResponseTimeout bounds the wait for response headers.
	defaultResponseTimeout time.Duration = 10 * time.Second
	// defaultTotalTimeout bounds the whole call, body included.
	defaultTotalTimeout time.Duration = 30 * time.Second
	// defaultKeepAlive is the TCP keep-alive probe interval.
	defaultKeepAlive time.Duration = 30 * time.Second
	// defaultMaxResponseSize is a deliberately modest ceiling — a client that
	// genuinely needs more should have to say so.
	defaultMaxResponseSize int64 = 8 << 20 // 8 MiB
	// defaultMaxRedirects is small because an API client following a long
	// redirect chain is nearly always a misconfiguration.
	defaultMaxRedirects int = 3
)

// withDefaults returns a copy of cfg with every unset field filled.
//
// It takes and returns a value rather than mutating through a pointer so the
// caller's own Config is never modified behind its back: a client must not be
// able to change the configuration its caller still holds.
func withDefaults(cfg Config) Config {
	//: a zero duration means "unset", so each field falls back independently.
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = corenet.DurationValue(defaultDialTimeout)
	}
	//: the handshake is bounded separately from the dial.
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = corenet.DurationValue(defaultHandshakeTimeout)
	}
	//: headers must arrive well before the total budget expires.
	if cfg.ResponseTimeout == 0 {
		cfg.ResponseTimeout = corenet.DurationValue(defaultResponseTimeout)
	}
	//: the total budget covers the body too.
	if cfg.TotalTimeout == 0 {
		cfg.TotalTimeout = corenet.DurationValue(defaultTotalTimeout)
	}
	//: an unset ceiling must not be read as "unbounded".
	if cfg.MaxResponseSize <= 0 {
		cfg.MaxResponseSize = defaultMaxResponseSize
	}
	//: zero means "unset" here; a caller refusing redirects passes a negative.
	if cfg.MaxRedirects == 0 {
		cfg.MaxRedirects = defaultMaxRedirects
	}
	//: every unset field now carries a safe value.
	return cfg
}
