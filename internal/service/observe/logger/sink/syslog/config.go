// Package syslog — holds the Config struct consumed by
// NewWithConfig. Pulled into its own file per the SDK's
// one-exported-struct-per-file convention.
package syslog

import "net"

// Config tunes the syslog sink at construction time. Every field is
// optional — the zero-value Config produces a sink using net.Dial as the
// dialer, which is the pre-existing behaviour preserved by New.
type Config struct {
	// Dialer, when non-nil, replaces net.Dial for the connection setup.
	// Callers whose operational environment exposes a hostile network
	// surface (e.g. a public-facing admin endpoint that takes a user-
	// controllable syslog destination) plug an allowlisted dialer here
	// to prevent SSRF against internal services or cloud-metadata
	// endpoints (CWE-918). Default nil preserves legacy net.Dial.
	Dialer func(network, addr string) (conn net.Conn, err error)
}
