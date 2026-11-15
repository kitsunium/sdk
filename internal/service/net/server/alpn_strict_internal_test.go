// Package server — alpnStrict (#964): which listeners refuse a client whose
// ALPN offer names nothing they serve, and what the refusal hands crypto/tls.
package server

import (
	"crypto/tls"
	"slices"
	"testing"
)

// TestALPNStrict pins alpnStrict's decision table: a list holding h2 without
// http/1.1 refuses a client that offers ALPN and shares nothing with it,
// with a configuration whose list is the empty name alone; any other list
// keeps crypto/tls's own negotiation (no GetConfigForClient); a client that
// offers no ALPN, or a name the list holds, keeps the listener's own
// configuration.
func TestALPNStrict(t *testing.T) {
	type tc struct {
		name    string
		list    []string
		offer   []string
		strict  bool
		refused bool
	}
	tests := []tc{
		{name: "h2 alone, http/1.1 offered", list: []string{"h2"}, offer: []string{"http/1.1"}, strict: true, refused: true},
		{name: "h2 alone, unknown offered", list: []string{"h2"}, offer: []string{"spdy/3"}, strict: true, refused: true},
		{name: "h2 alone, h2 offered", list: []string{"h2"}, offer: []string{"http/1.1", "h2"}, strict: true},
		{name: "h2 alone, no ALPN", list: []string{"h2"}, strict: true},
		{name: "both", list: []string{"h2", "http/1.1"}, offer: []string{"http/1.1"}},
		{name: "http/1.1 alone", list: []string{"http/1.1"}, offer: []string{"h2"}},
		{name: "no list", offer: []string{"http/1.1"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := alpnStrict(&tls.Config{NextProtos: c.list, MinVersion: tls.VersionTLS12})
		if (cfg.GetConfigForClient != nil) != c.strict {
			t.Fatalf("GetConfigForClient set = %v, want %v", cfg.GetConfigForClient != nil, c.strict)
		}
		if !c.strict {
			return
		}
		got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{SupportedProtos: c.offer})
		if err != nil {
			t.Fatalf("GetConfigForClient: %v", err)
		}
		if !c.refused {
			if got != nil {
				t.Fatalf("offer %q answered with a configuration of its own: %q", c.offer, got.NextProtos)
			}
			return
		}
		if got == nil || !slices.Equal(got.NextProtos, []string{""}) || got.GetConfigForClient != nil {
			t.Fatalf("offer %q: refusal %+v, want the empty name alone and no hook", c.offer, got)
		}
		if got.MinVersion != tls.VersionTLS12 {
			t.Fatalf("the refusal's floor is %#x, want the listener's", got.MinVersion)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
