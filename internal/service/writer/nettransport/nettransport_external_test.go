package nettransport_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/internal/service/writer/nettransport"
)

func Test_protocolsRegistered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  writer.Name
	}{
		{"tcp is registered", "tcp"},
		{"udp is registered", "udp"},
		{"http is registered", "http"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: importing the package self-registers all three protocol factories.
			if !tc.key.Known() {
				t.Errorf("%s: writer key %q not registered", tc.name, tc.key)
			}
		})
	}
}
