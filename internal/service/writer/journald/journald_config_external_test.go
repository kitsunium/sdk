package journald_test

import (
	"net"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	jd "github.com/kitsunium/sdk/internal/service/writer/journald"
)

func Test_Config_resolvesViaOpen(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"config with an injected dialer builds a sink"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: inject an in-memory pipe so Open connects with no real journald.
			clientEnd, serverEnd := net.Pipe()
			t.Cleanup(func() { closeIgnore(serverEnd.Close()) })
			cfg := jd.Config{Dialer: func(_, _ string) (net.Conn, error) { return clientEnd, nil }}
			sink, err := writer.Open("journald", cfg)
			//: a Config must drive the registered factory end-to-end.
			if err != nil || sink == nil {
				t.Fatalf("%s: Open err=%v sink=%v want sink+nil", tc.name, err, sink)
			}
			//: Close releases the (piped) connection.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}
