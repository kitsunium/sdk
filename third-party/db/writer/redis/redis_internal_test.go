package redis

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_redisFactory_Name(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"reports the canonical key", "redis"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the factory must report the key it registered under.
			if got := (&redisFactory{}).Name(); string(got) != tc.want {
				t.Errorf("%s: Name()=%q want %q", tc.name, got, tc.want)
			}
		})
	}
}

func Test_redisFactory_Open(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cfg      writer.Config
		wantNil  bool
		wantCode errs.Code
	}{
		{"wrong config type rejected", "nope", true, writer.CodeWriterConfigInvalid},
		{"missing stream rejected", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock"}, true, CodeRedisClientInitFailed},
		{"valid config builds a sink", writer.RedisStreamConfig{SocketPath: "/tmp/r.sock", Stream: "logs"}, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink, err := (&redisFactory{}).Open(tc.cfg)
			//: error arm — nil sink and the documented code.
			if tc.wantNil {
				if sink != nil || !errs.HasCode(err, tc.wantCode) {
					t.Fatalf("%s: sink=%v err=%v want nil+code %v", tc.name, sink, err, tc.wantCode)
				}
				return
			}
			//: happy arm — a usable sink over a lazy client.
			if err != nil || sink == nil {
				t.Fatalf("%s: sink=%v err=%v want sink+nil", tc.name, sink, err)
			}
			//: Close drains the (empty) chain and releases the lazy client.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: Close: %v", tc.name, cerr)
			}
		})
	}
}
