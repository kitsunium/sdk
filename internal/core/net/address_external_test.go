// Package net_test — the listen address value.
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_AddressValue_String pins the single-token rendering. The form goes into
// log fields and error metadata, where a reader needs the family and the target
// together — "0.0.0.0:8080" alone does not say whether it is TCP or UDP.
func Test_AddressValue_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		addr corenet.AddressValue
		want string
	}
	tests := []tc{
		{"a TCP host and port", corenet.AddressValue{Network: "tcp", Addr: "127.0.0.1:8080"}, "tcp://127.0.0.1:8080"},
		{"a UDP wildcard bind", corenet.AddressValue{Network: "udp", Addr: ":53"}, "udp://:53"},
		{"a Unix socket path", corenet.AddressValue{Network: "unix", Addr: "/run/api.sock"}, "unix:///run/api.sock"},
		{"an IPv6 literal", corenet.AddressValue{Network: "tcp6", Addr: "[::1]:443"}, "tcp6://[::1]:443"},
		//: the zero value must still render, so an unset address in a log line
		//: reads as an address rather than as an empty string.
		{"the zero value", corenet.AddressValue{}, "://"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.addr.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
