package nettransport_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	nt "github.com/kitsunium/sdk/internal/service/writer/nettransport"
)

func Test_NetConfig_resolvesViaOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  writer.Name
		cfg  nt.NetConfig
	}{
		{"http config builds a sink", "http", nt.NetConfig{Address: "http://collector.local/ingest"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a NetConfig must drive the registered factory end-to-end.
			sink, err := writer.Open(tc.key, tc.cfg)
			if err != nil || sink == nil {
				t.Fatalf("%s: Open err=%v sink=%v want sink+nil", tc.name, err, sink)
			}
			//: release the constructed chain.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}
