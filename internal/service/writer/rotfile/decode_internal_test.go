package rotfile

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the factory satisfies the optional config Decoder
// : extension (kept in the test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Decoder = (*rotFileFactory)(nil)

func Test_rotFileFactory_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		raw       map[string]any
		wantErr   bool
		wantEvery time.Duration
		wantAge   int
	}
	tests := []tc{
		{name: "minimal path only", raw: map[string]any{"path": "/p"}},
		{name: "missing path errors", raw: map[string]any{}, wantErr: true},
		{name: "bad scalar errors", raw: map[string]any{"path": "/p", "max_bytes": "x"}, wantErr: true},
		{name: "interval enables 7d retention", raw: map[string]any{"path": "/p", "rotate_every": "24h"}, wantEvery: 24 * time.Hour, wantAge: 7},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg, err := (&rotFileFactory{}).Decode(c.raw)
		//: failure arm — redacted decode sentinel + nil config.
		if c.wantErr {
			//: a malformed shape must surface the decode sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) || cfg != nil {
				t.Fatalf("%s: err=%v cfg=%v want decode-failed+nil", c.name, err, cfg)
			}
			return
		}
		got, ok := cfg.(Config)
		//: happy arm — the typed Config Open asserts.
		if err != nil || !ok {
			t.Fatalf("%s: err=%v cfg=%T want Config", c.name, err, cfg)
		}
		//: the interval cadence and defaulted retention must match.
		if got.RotateEvery != c.wantEvery || got.MaxAgeDays != c.wantAge {
			t.Errorf("%s: got {every=%v age=%d} want {%v %d}", c.name, got.RotateEvery, got.MaxAgeDays, c.wantEvery, c.wantAge)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeRotScalars(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    Config
	}
	tests := []tc{
		{name: "maps size and compress", raw: map[string]any{"max_bytes": 1024, "compress": true}, want: Config{MaxBytes: 1024, Compress: true}},
		{name: "bad age errors", raw: map[string]any{"max_age_days": "old"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got Config
		err := decodeRotScalars(c.raw, &got)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a malformed scalar must surface the decode sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the recognised scalars land on the config.
		if err != nil || got.MaxBytes != c.want.MaxBytes || got.Compress != c.want.Compress {
			t.Errorf("%s: err=%v got %+v want %+v", c.name, err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeReqString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    string
	}
	tests := []tc{
		{name: "valid string", raw: map[string]any{"path": "/p"}, want: "/p"},
		{name: "absent rejected", raw: map[string]any{}, wantErr: true},
		{name: "empty rejected", raw: map[string]any{"path": ""}, wantErr: true},
		{name: "non-string rejected", raw: map[string]any{"path": 7}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst string
		err := decodeReqString(c.raw, "path", &dst)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a malformed mandatory string must surface the sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the validated value is assigned.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%q want %q", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeInt64(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    int64
	}
	tests := []tc{
		{name: "absent keeps default", raw: map[string]any{}, want: 0},
		{name: "int64 value", raw: map[string]any{"max_bytes": int64(8)}, want: 8},
		{name: "float64 value", raw: map[string]any{"max_bytes": float64(9)}, want: 9},
		{name: "non-numeric rejected", raw: map[string]any{"max_bytes": "big"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst int64
		err := decodeInt64(c.raw, "max_bytes", &dst)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a non-numeric value must surface the sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the coerced value is assigned.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%d want %d", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeInt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    int
	}
	tests := []tc{
		{name: "absent keeps default", raw: map[string]any{}, want: 0},
		{name: "int value", raw: map[string]any{"n": 5}, want: 5},
		{name: "non-numeric rejected", raw: map[string]any{"n": "five"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst int
		err := decodeInt(c.raw, "n", &dst)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a non-numeric value must surface the sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the narrowed value is assigned.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%d want %d", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeBool(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    bool
	}
	tests := []tc{
		{name: "absent keeps default", raw: map[string]any{}, want: false},
		{name: "true value", raw: map[string]any{"compress": true}, want: true},
		{name: "non-bool rejected", raw: map[string]any{"compress": "yes"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst bool
		err := decodeBool(c.raw, "compress", &dst)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a non-bool value must surface the sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the toggle is assigned.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%v want %v", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeRotMinLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    level.Level
	}
	tests := []tc{
		{name: "warn name", raw: map[string]any{"min_level": "warn"}, want: level.Warn},
		{name: "non-string rejected", raw: map[string]any{"min_level": 3}, wantErr: true},
		{name: "unknown rejected", raw: map[string]any{"min_level": "loud"}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: seed a non-zero floor so the absent-key path is observable elsewhere.
		dst := level.Error
		err := decodeRotMinLevel(c.raw, &dst)
		//: failure arm — redacted decode sentinel.
		if c.wantErr {
			//: a malformed level must surface the sentinel.
			if !errs.HasCode(err, CodeRotFileDecodeFailed) {
				t.Errorf("%s: err=%v want decode-failed", c.name, err)
			}
			return
		}
		//: happy arm — the parsed floor is assigned.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%v want %v", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeRotateEvery(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  map[string]any
		want time.Duration
	}
	tests := []tc{
		{name: "absent disables", raw: map[string]any{}, want: 0},
		{name: "valid enables", raw: map[string]any{"rotate_every": "24h"}, want: 24 * time.Hour},
		{name: "non-positive disables", raw: map[string]any{"rotate_every": "0s"}, want: 0},
		{name: "malformed disables", raw: map[string]any{"rotate_every": "nope"}, want: 0},
		{name: "non-string disables", raw: map[string]any{"rotate_every": 9}, want: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst time.Duration
		//: decodeRotateEvery is tolerant — it never returns an error.
		decodeRotateEvery(c.raw, &dst)
		//: the tolerant cadence must match the expectation.
		if dst != c.want {
			t.Errorf("%s: dst=%v want %v", c.name, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_asInt64(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     any
		want   int64
		wantOk bool
	}
	tests := []tc{
		{name: "int", in: 3, want: 3, wantOk: true},
		{name: "int64", in: int64(4), want: 4, wantOk: true},
		{name: "uint64", in: uint64(5), want: 5, wantOk: true},
		{name: "float64", in: float64(6), want: 6, wantOk: true},
		{name: "string miss", in: "x", want: 0, wantOk: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := asInt64(c.in)
		//: the coercion result and success flag must both match.
		if got != c.want || ok != c.wantOk {
			t.Errorf("%s: asInt64=%d,%v want %d,%v", c.name, got, ok, c.want, c.wantOk)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeInvalid(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"sentinel carries the decode code and no value"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		err := decodeInvalid()
		//: the helper must surface the redacted decode sentinel.
		if !errs.HasCode(err, CodeRotFileDecodeFailed) {
			t.Errorf("decodeInvalid()=%v want RotFileDecodeFailed code", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
