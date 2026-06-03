package nettransport

import (
	"net"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the factory satisfies the registry port (kept in the
// : test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Factory = (*netFactory)(nil)

func Test_netFactory_Name(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		proto string
		want  writer.Name
	}{
		{"tcp key", protoTCP, "tcp"},
		{"udp key", protoUDP, "udp"},
		{"http key", protoHTTP, "http"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the factory must report the key it registered under.
			if got := (&netFactory{proto: tc.proto}).Name(); got != tc.want {
				t.Errorf("%s: Name()=%q want %q", tc.name, got, tc.want)
			}
		})
	}
}

func Test_netFactory_Open(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		proto    string
		cfg      writer.Config
		wantNil  bool
		wantCode errs.Code
	}{
		{"wrong config type rejected", protoTCP, "not a NetConfig", true, writer.CodeWriterConfigInvalid},
		{"empty address rejected", protoTCP, NetConfig{}, true, CodeNetTransportDialFailed},
		{"http builds without dialing", protoHTTP, NetConfig{Address: "http://collector.local/ingest"}, false, 0},
		{"tcp dialer failure surfaces dial code", protoTCP, NetConfig{Address: "x:1", Dialer: failingDialer}, true, CodeNetTransportDialFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink, err := (&netFactory{proto: tc.proto}).Open(tc.cfg)
			//: error arm — nil sink and the documented code.
			if tc.wantNil {
				if sink != nil || !errs.HasCode(err, tc.wantCode) {
					t.Errorf("%s: sink=%v err=%v want nil+code %v", tc.name, sink, err, tc.wantCode)
				}
				return
			}
			//: happy arm — a usable sink and no error.
			if err != nil || sink == nil {
				t.Fatalf("%s: sink=%v err=%v want sink+nil", tc.name, sink, err)
			}
			//: release the constructed chain.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}

func Test_netFactory_build(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		proto   string
		cfg     NetConfig
		wantErr bool
	}{
		{"http builds a stateless seam", protoHTTP, NetConfig{Address: "http://x/y"}, false},
		{"tcp dial failure propagates", protoTCP, NetConfig{Address: "x:1", Dialer: failingDialer}, true},
		{"tcp dial succeeds returns usable sink", protoTCP, NetConfig{Address: "h:1", Dialer: pipeDialer()}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink, err := (&netFactory{proto: tc.proto}).build(tc.cfg)
			//: error arm — dial failure surfaces, nil sink.
			if tc.wantErr {
				if sink != nil || !errs.HasCode(err, CodeNetTransportDialFailed) {
					t.Errorf("%s: sink=%v err=%v want nil+dial-failed", tc.name, sink, err)
				}
				return
			}
			//: happy arm — a usable sink, no error.
			if err != nil || sink == nil {
				t.Fatalf("%s: sink=%v err=%v want sink+nil", tc.name, sink, err)
			}
			//: release the constructed chain.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}

// failingDialer is a Dialer that always fails, exercising the dial-error branch
// deterministically without touching the network.
func failingDialer(_, _ string) (net.Conn, error) {
	//: a fixed failure drives the dial-error path with no real socket.
	return nil, errNetBoom{}
}

// pipeDialer returns a Dialer that hands back one end of an in-memory net.Pipe, so
// build's dial-succeeds path (nettransport.go:88) runs deterministically with no
// real socket. The peer end is closed immediately — the test only constructs and
// closes the sink, never sends, so a live reader is unnecessary and the seam's
// Close releases the returned end cleanly.
func pipeDialer() func(network, addr string) (net.Conn, error) {
	return func(_, _ string) (net.Conn, error) {
		//: a connected pipe end is a usable net.Conn the seam can wrap and close.
		clientEnd, serverEnd := net.Pipe()
		//: drop the peer end up front; nothing reads it in this construction-only test.
		swallowErr(serverEnd.Close())
		return clientEnd, nil
	}
}
