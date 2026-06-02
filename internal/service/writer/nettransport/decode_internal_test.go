package nettransport

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the factory satisfies the optional config Decoder
// : extension (kept in the test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Decoder = (*netFactory)(nil)

func Test_netFactory_Decode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    NetConfig
	}{
		{"address only", map[string]any{"address": "h:1"}, false, NetConfig{Address: "h:1"}},
		{"all keys", map[string]any{"address": "h:1", "min_level": "warn", "buffer_size": 8}, false, NetConfig{Address: "h:1", MinLevel: level.Warn, BufferSize: 8}},
		{"missing address", map[string]any{}, true, NetConfig{}},
		{"non-string address", map[string]any{"address": 7}, true, NetConfig{}},
		{"bad min_level", map[string]any{"address": "h:1", "min_level": "loud"}, true, NetConfig{}},
		{"bad buffer_size", map[string]any{"address": "h:1", "buffer_size": "big"}, true, NetConfig{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := (&netFactory{proto: protoTCP}).Decode(tc.raw)
			//: error arm — redacted shared sentinel, nil config.
			if tc.wantErr {
				if cfg != nil || !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
					t.Fatalf("%s: cfg=%v err=%v want nil+config-invalid", tc.name, cfg, err)
				}
				return
			}
			got, ok := cfg.(NetConfig)
			//: happy arm — the typed NetConfig with the mapped fields.
			if err != nil || !ok || got.Address != tc.want.Address || got.MinLevel != tc.want.MinLevel || got.BufferSize != tc.want.BufferSize {
				t.Errorf("%s: got %+v err=%v want %+v", tc.name, got, err, tc.want)
			}
		})
	}
}

func Test_netFactory_invalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sentinel carries the shared config-invalid code"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the helper must surface the shared config-invalid sentinel.
			if err := (&netFactory{proto: protoUDP}).invalid(); !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: invalid()=%v want WriterConfigInvalid", tc.name, err)
			}
		})
	}
}

func Test_decodeStr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]any
		want string
		ok   bool
	}{
		{"absent keeps default", map[string]any{}, "", true},
		{"valid string", map[string]any{"address": "h:1"}, "h:1", true},
		{"non-string fails", map[string]any{"address": 7}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var dst string
			ok := decodeStr(tc.raw, "address", &dst)
			//: the success flag and assigned value must both match.
			if ok != tc.ok || dst != tc.want {
				t.Errorf("%s: ok=%v dst=%q want %v,%q", tc.name, ok, dst, tc.ok, tc.want)
			}
		})
	}
}

func Test_decodeLevelKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]any
		want level.Level
		ok   bool
	}{
		{"absent keeps seed", map[string]any{}, level.Error, true},
		{"warn name", map[string]any{"min_level": "warn"}, level.Warn, true},
		{"non-string fails", map[string]any{"min_level": 3}, level.Error, false},
		{"unknown fails", map[string]any{"min_level": "loud"}, level.Error, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dst := level.Error
			ok := decodeLevelKey(tc.raw, &dst)
			//: the success flag and (possibly unchanged) floor must match.
			if ok != tc.ok || dst != tc.want {
				t.Errorf("%s: ok=%v dst=%v want %v,%v", tc.name, ok, dst, tc.ok, tc.want)
			}
		})
	}
}

func Test_decodeIntKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]any
		want int
		ok   bool
	}{
		{"absent keeps default", map[string]any{}, 0, true},
		{"int value", map[string]any{"buffer_size": 5}, 5, true},
		{"float64 value", map[string]any{"buffer_size": float64(6)}, 6, true},
		{"non-numeric fails", map[string]any{"buffer_size": "big"}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var dst int
			ok := decodeIntKey(tc.raw, "buffer_size", &dst)
			//: the success flag and assigned value must both match.
			if ok != tc.ok || dst != tc.want {
				t.Errorf("%s: ok=%v dst=%d want %v,%d", tc.name, ok, dst, tc.ok, tc.want)
			}
		})
	}
}
