// Package client — the outbound client configuration.
package client

import corenet "github.com/kitsunium/sdk/internal/core/net"

// Config describes an outbound client. Durations use corenet.DurationValue so a
// configuration file can say "5s" rather than a nanosecond count.
type Config struct {
	// BaseURL is the origin every relative path resolves against.
	BaseURL string `json:"base_url"`
	// DialTimeout bounds establishing the TCP connection.
	DialTimeout corenet.DurationValue `json:"dial_timeout"`
	// HandshakeTimeout bounds the TLS handshake.
	HandshakeTimeout corenet.DurationValue `json:"handshake_timeout"`
	// ResponseTimeout bounds the wait for response headers after the request
	// has been written. It is separate from TotalTimeout so a slow upstream is
	// distinguishable from a large body.
	ResponseTimeout corenet.DurationValue `json:"response_timeout"`
	// TotalTimeout bounds the whole call, body included.
	TotalTimeout corenet.DurationValue `json:"total_timeout"`
	// MaxResponseSize caps the response body in bytes. Exceeding it fails the
	// call; the body is never truncated.
	MaxResponseSize int64 `json:"max_response_size"`
	// MaxRedirects caps the redirect chain. Every hop is re-authorised by the
	// policy, so a redirect cannot walk out of the allowed surface.
	MaxRedirects int `json:"max_redirects"`
	// DefaultHeaders are added to every request that does not already set them.
	DefaultHeaders map[string]string `json:"default_headers"`
}
