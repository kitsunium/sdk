package journald

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the factory satisfies the optional config Decoder
// : extension (kept in the test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Decoder = (*journaldFactory)(nil)

func Test_journaldFactory_Decode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    Config
	}{
		{"empty config is valid", map[string]any{}, false, Config{}},
		{"all keys", map[string]any{"socket_path": "/s", "min_level": "warn", "buffer_size": 8}, false, Config{SocketPath: "/s", MinLevel: level.Warn, BufferSize: 8}},
		{"non-string socket", map[string]any{"socket_path": 7}, true, Config{}},
		{"bad min_level", map[string]any{"min_level": "loud"}, true, Config{}},
		{"bad buffer_size", map[string]any{"buffer_size": "big"}, true, Config{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := (&journaldFactory{}).Decode(tc.raw)
			//: error arm — redacted shared sentinel, nil config.
			if tc.wantErr {
				if cfg != nil || !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
					t.Fatalf("%s: cfg=%v err=%v want nil+config-invalid", tc.name, cfg, err)
				}
				return
			}
			got, ok := cfg.(Config)
			//: happy arm — the typed config with the mapped fields (compared
			//: field-wise: Config has func fields and is not comparable).
			if err != nil || !ok || got.SocketPath != tc.want.SocketPath ||
				got.MinLevel != tc.want.MinLevel || got.BufferSize != tc.want.BufferSize {
				t.Errorf("%s: got %+v err=%v want %+v", tc.name, got, err, tc.want)
			}
		})
	}
}

func Test_configInvalid(t *testing.T) {
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
			if err := configInvalid(); !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: configInvalid()=%v want WriterConfigInvalid", tc.name, err)
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
		{"valid string", map[string]any{"socket_path": "/s"}, "/s", true},
		{"non-string fails", map[string]any{"socket_path": 7}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var dst string
			ok := decodeStr(tc.raw, "socket_path", &dst)
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
