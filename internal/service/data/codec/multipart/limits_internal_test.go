package multipart

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestLimitsResolve pins ADR 0031 on this type: every zero field collapses to
// its documented default (never to "unlimited"), a caller-chosen bound is kept
// verbatim, and a negative bound is refused rather than guessed at.
func TestLimitsResolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      LimitsConfig
		want    LimitsConfig
		wantErr bool
	}
	tests := []tc{
		{
			name: "zero collapses to the defaults",
			in:   LimitsConfig{},
			want: LimitsConfig{DefaultMaxPartBytes, DefaultMaxParts, DefaultMaxTotalBytes},
		},
		{
			name: "chosen bounds are kept",
			in:   LimitsConfig{MaxPartBytes: 7, MaxParts: 3, MaxTotalBytes: 11},
			want: LimitsConfig{7, 3, 11},
		},
		{
			name: "partial zeros fill from the defaults",
			in:   LimitsConfig{MaxParts: 3},
			want: LimitsConfig{DefaultMaxPartBytes, 3, DefaultMaxTotalBytes},
		},
		{name: "negative part bound refused", in: LimitsConfig{MaxPartBytes: -1}, wantErr: true},
		{name: "negative count bound refused", in: LimitsConfig{MaxParts: -1}, wantErr: true},
		{name: "negative total bound refused", in: LimitsConfig{MaxTotalBytes: -1}, wantErr: true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := tc.in.resolve()
		if tc.wantErr {
			if !errs.HasReason(err, "LIMITS_INVALID") {
				t.Errorf("%s: expected LIMITS_INVALID, got %v", tc.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: resolve err=%v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %+v want %+v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestNegativeKnob pins which knob a multi-mistake struct names — declaration
// order, so the message is deterministic.
func TestNegativeKnob(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		in    LimitsConfig
		want  string
		found bool
	}
	tests := []tc{
		{"none", LimitsConfig{MaxParts: 1}, "", false},
		{"part bytes", LimitsConfig{MaxPartBytes: -1}, "MaxPartBytes", true},
		{"parts", LimitsConfig{MaxParts: -1}, "MaxParts", true},
		{"total bytes", LimitsConfig{MaxTotalBytes: -1}, "MaxTotalBytes", true},
		{"first in declaration order wins", LimitsConfig{MaxPartBytes: -1, MaxParts: -1}, "MaxPartBytes", true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, found := tc.in.negativeKnob()
		if found != tc.found || got != tc.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.name, got, found, tc.want, tc.found)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestCounterAdmitPart pins each of the three ceilings independently, so a
// change that silently merges two of them shows up here.
func TestCounterAdmitPart(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		limits  LimitsConfig
		sizes   []int64
		wantErr bool
	}
	tests := []tc{
		{"within every bound", LimitsConfig{4, 4, 16}, []int64{1, 2, 3}, false},
		{"exactly at the part bound", LimitsConfig{4, 4, 16}, []int64{4}, false},
		{"exactly at the count bound", LimitsConfig{4, 2, 16}, []int64{1, 1}, false},
		{"exactly at the total bound", LimitsConfig{4, 4, 4}, []int64{2, 2}, false},
		{"part bound crossed", LimitsConfig{4, 4, 64}, []int64{5}, true},
		{"count bound crossed", LimitsConfig{4, 2, 64}, []int64{1, 1, 1}, true},
		{"total bound crossed", LimitsConfig{4, 8, 5}, []int64{3, 3}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := counter{limits: tc.limits}
		var err error
		for _, size := range tc.sizes {
			if err = c.admitPart(size); err != nil {
				break
			}
		}
		if tc.wantErr {
			if !errs.HasReason(err, "LIMIT_EXCEEDED") {
				t.Errorf("%s: expected LIMIT_EXCEEDED, got %v", tc.name, err)
			}
			return
		}
		if err != nil {
			t.Errorf("%s: unexpected refusal %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestDefaultLimitsAreResolved guards the singleton: the registered codec must
// carry the resolved defaults, never a zero Limits that would compare every
// size against zero and refuse everything.
func TestDefaultLimitsAreResolved(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  int64
		want int64
	}
	resolved := defaultLimits()
	tests := []tc{
		{"part bytes", resolved.MaxPartBytes, DefaultMaxPartBytes},
		{"parts", int64(resolved.MaxParts), int64(DefaultMaxParts)},
		{"total bytes", resolved.MaxTotalBytes, DefaultMaxTotalBytes},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if tc.got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, tc.got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
