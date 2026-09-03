package net_test

import (
	"encoding/json"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// durationCase describes one decoding scenario for DurationValue.
type durationCase struct {
	name    string
	raw     string
	want    time.Duration
	wantErr bool
}

// TestDurationDecodesBothForms pins the reason this type exists: the SDK config
// loader decodes through a JSON round-trip, and a bare time.Duration would force
// an operator to write 30000000000 in a YAML file to mean thirty seconds.
func TestDurationDecodesBothForms(t *testing.T) {
	t.Parallel()
	cases := []durationCase{
		{name: "human duration string", raw: `"30s"`, want: 30 * time.Second},
		{name: "compound duration string", raw: `"1m30s"`, want: 90 * time.Second},
		{name: "sub-second duration string", raw: `"250ms"`, want: 250 * time.Millisecond},
		{name: "raw nanosecond count", raw: `30000000000`, want: 30 * time.Second},
		{name: "zero nanoseconds", raw: `0`, want: 0},
		{name: "empty string means unset", raw: `""`, want: 0},
		{name: "nonsense string", raw: `"thirty seconds"`, wantErr: true},
		{name: "unitless number in a string", raw: `"30"`, wantErr: true},
		{name: "non-numeric bare token", raw: `true`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runDurationCase(t, tc)
		})
	}
}

// runDurationCase decodes one durationCase and checks the outcome.
func runDurationCase(t *testing.T, tc durationCase) {
	t.Helper()
	var got corenet.DurationValue
	err := json.Unmarshal([]byte(tc.raw), &got)
	if tc.wantErr {
		if !errs.HasCode(err, corenet.CodeInvalidDuration) {
			t.Fatalf("expected INVALID_DURATION, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Duration() != tc.want {
		t.Fatalf("decoded %v, want %v", got.Duration(), tc.want)
	}
}

// TestDurationRoundTripsReadable pins that a re-encoded configuration stays
// legible instead of degrading into a nanosecond count.
func TestDurationRoundTripsReadable(t *testing.T) {
	t.Parallel()
	original := corenet.DurationValue(90 * time.Second)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `"1m30s"` {
		t.Fatalf("encoded %s, want \"1m30s\"", encoded)
	}
	var back corenet.DurationValue
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != original {
		t.Fatalf("round trip changed the value: %v != %v", back, original)
	}
}

// TestDurationDecodesInsideAStruct pins the real usage shape: a config struct
// with json tags, which is the only mapping mechanism the SDK config loader has.
func TestDurationDecodesInsideAStruct(t *testing.T) {
	t.Parallel()
	type conf struct {
		Dial corenet.DurationValue `json:"dial_timeout"`
	}
	var c conf
	if err := json.Unmarshal([]byte(`{"dial_timeout":"2s"}`), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Dial.Duration() != 2*time.Second {
		t.Fatalf("Dial = %v, want 2s", c.Dial.Duration())
	}
	if c.Dial.String() != "2s" {
		t.Fatalf("String = %q, want \"2s\"", c.Dial.String())
	}
}
