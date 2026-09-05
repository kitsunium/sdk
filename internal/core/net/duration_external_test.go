// Package net_test — the JSON-friendly duration value.
package net_test

import (
	"encoding/json"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_DurationValue_UnmarshalJSON pins the reason this type exists: the SDK
// config loader decodes through a JSON round-trip, and a bare time.Duration
// would force an operator to write 30000000000 in a YAML file to mean thirty
// seconds. Both forms must decode, and anything else must be refused rather
// than silently taken as zero — a timeout that quietly became 0 is worse than
// one that failed to load.
func Test_DurationValue_UnmarshalJSON(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}
	tests := []tc{
		{name: "a human duration string", raw: `"30s"`, want: 30 * time.Second},
		{name: "a compound duration string", raw: `"1m30s"`, want: 90 * time.Second},
		{name: "a sub-second duration string", raw: `"250ms"`, want: 250 * time.Millisecond},
		{name: "a negative duration string", raw: `"-5s"`, want: -5 * time.Second},
		{name: "a raw nanosecond count", raw: `30000000000`, want: 30 * time.Second},
		{name: "zero nanoseconds", raw: `0`, want: 0},
		{name: "a negative nanosecond count", raw: `-1000`, want: -1000},
		{name: "an empty string means unset", raw: `""`, want: 0},
		{name: "nonsense in a string", raw: `"thirty seconds"`, wantErr: true},
		{name: "a unitless number in a string", raw: `"30"`, wantErr: true},
		{name: "a non-numeric bare token", raw: `true`, wantErr: true},
		{name: "a floating-point nanosecond count", raw: `1.5`, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got corenet.DurationValue
		err := json.Unmarshal([]byte(c.raw), &got)
		if c.wantErr {
			//: the refusal must be typed, so a config loader can report which
			//: field was wrong rather than "invalid JSON".
			if !errs.HasCode(err, corenet.CodeInvalidDuration) {
				t.Fatalf("Unmarshal(%s) = %v, want INVALID_DURATION", c.raw, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Unmarshal(%s) = %v, want nil", c.raw, err)
		}
		if got.Duration() != c.want {
			t.Fatalf("Unmarshal(%s) decoded %v, want %v", c.raw, got.Duration(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DurationValue_MarshalJSON pins that a re-encoded configuration stays
// legible instead of degrading into a nanosecond count, and that what it emits
// decodes back to the same value.
func Test_DurationValue_MarshalJSON(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   corenet.DurationValue
		want string
	}
	tests := []tc{
		{"a compound duration", corenet.DurationValue(90 * time.Second), `"1m30s"`},
		{"a whole second", corenet.DurationValue(time.Second), `"1s"`},
		{"a sub-second duration", corenet.DurationValue(250 * time.Millisecond), `"250ms"`},
		{"the zero value", corenet.DurationValue(0), `"0s"`},
		{"a negative duration", corenet.DurationValue(-5 * time.Second), `"-5s"`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		encoded, err := json.Marshal(c.in)
		if err != nil {
			t.Fatalf("Marshal(%v) = %v, want nil", c.in.Duration(), err)
		}
		if string(encoded) != c.want {
			t.Fatalf("Marshal(%v) = %s, want %s", c.in.Duration(), encoded, c.want)
		}
		//: what we emit must be something we accept, or a written-back config
		//: would fail to reload.
		var back corenet.DurationValue
		if uerr := json.Unmarshal(encoded, &back); uerr != nil {
			t.Fatalf("re-decoding %s = %v, want nil", encoded, uerr)
		}
		if back != c.in {
			t.Errorf("the round trip changed %v into %v", c.in.Duration(), back.Duration())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DurationValue_String pins the canonical rendering, which is what both
// MarshalJSON and every log field go through.
func Test_DurationValue_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   corenet.DurationValue
		want string
	}
	tests := []tc{
		{"a compound duration", corenet.DurationValue(90 * time.Second), "1m30s"},
		{"the zero value", corenet.DurationValue(0), "0s"},
		{"a sub-second duration", corenet.DurationValue(1500 * time.Microsecond), "1.5ms"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.in.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DurationValue_Duration pins the conversion back to the stdlib type, and
// the real usage shape behind it: a config struct with json tags, which is the
// only mapping mechanism the SDK config loader has.
func Test_DurationValue_Duration(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  string
		want time.Duration
	}
	tests := []tc{
		{"a readable field", `{"dial_timeout":"2s"}`, 2 * time.Second},
		{"a nanosecond field", `{"dial_timeout":2000000000}`, 2 * time.Second},
		{"an absent field", `{}`, 0},
		{"an explicitly empty field", `{"dial_timeout":""}`, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		type conf struct {
			Dial corenet.DurationValue `json:"dial_timeout"`
		}
		var cfg conf
		if err := json.Unmarshal([]byte(c.raw), &cfg); err != nil {
			t.Fatalf("Unmarshal(%s) = %v, want nil", c.raw, err)
		}
		if got := cfg.Dial.Duration(); got != c.want {
			t.Fatalf("Dial = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
