// Package net — the outbound client configuration.
package net

// ClientConfig describes an outbound client. Durations use DurationValue so a
// configuration file can say "5s" rather than a nanosecond count.
//
// It sits in core beside LimitsValue and TimeoutsValue, which carry the same
// kind of knobs for the inbound half. Keeping both faces' configuration in one
// layer is what lets a consumer decode a whole network stanza against the
// contract package alone, without importing an implementation to name the type
// of a field it is setting.
type ClientConfig struct {
	// BaseURL is the origin every relative path resolves against.
	BaseURL string `json:"base_url"`
	// DialTimeout bounds establishing the TCP connection.
	DialTimeout DurationValue `json:"dial_timeout"`
	// HandshakeTimeout bounds the TLS handshake.
	HandshakeTimeout DurationValue `json:"handshake_timeout"`
	// ResponseTimeout bounds the wait for response headers after the request
	// has been written. It is separate from TotalTimeout so a slow upstream is
	// distinguishable from a large body.
	ResponseTimeout DurationValue `json:"response_timeout"`
	// TotalTimeout bounds the whole call, body included.
	TotalTimeout DurationValue `json:"total_timeout"`
	// MaxResponseSize caps the response body in bytes. A body of exactly this
	// size is admitted; only one past it fails the call, and the body is never
	// truncated.
	MaxResponseSize int64 `json:"max_response_size"`
	// MaxRedirects caps the redirect chain. Every hop is re-authorised by the
	// policy, so a redirect cannot walk out of the allowed surface.
	MaxRedirects int `json:"max_redirects"`
	// DefaultHeaders are added to every request that does not already set them.
	DefaultHeaders map[string]string `json:"default_headers"`
}
