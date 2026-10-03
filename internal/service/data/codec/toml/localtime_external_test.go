// Package toml_test — the three local types: their text, both ways, and the
// instant each becomes in a zone.
package toml_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/toml"
)

// TestLocalDateText writes and reads a day.
func TestLocalDateText(t *testing.T) {
	t.Parallel()
	d := toml.LocalDate{Year: 7, Month: 3, Day: 9}
	//: padded to RFC 3339's widths.
	if d.String() != "0007-03-09" {
		t.Errorf("String = %q", d.String())
	}
	var back toml.LocalDate
	//: what String writes, UnmarshalText reads.
	if err := back.UnmarshalText([]byte(d.String())); err != nil || back != d {
		t.Errorf("UnmarshalText = %+v, %v", back, err)
	}
	//: a day the month does not have, another kind, trailing text.
	for _, bad := range []string{"2023-02-29", "1979-05-27T07:32:00", "1979-05-27x", "07:32:00", ""} {
		//: refused, as UNMARSHAL_FAILED.
		if err := back.UnmarshalText([]byte(bad)); !errs.HasReason(err, "UNMARSHAL_FAILED") {
			t.Errorf("UnmarshalText(%q) = %v", bad, err)
		}
	}
	//: midnight in the zone asked for.
	if got := d.AsTime(time.UTC); !got.Equal(time.Date(7, 3, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("AsTime = %v", got)
	}
}

// TestLocalTimeText writes and reads a time of day, with and without a
// precision.
func TestLocalTimeText(t *testing.T) {
	t.Parallel()
	type tc struct {
		value toml.LocalTime
		text  string
	}
	tests := []tc{
		{toml.LocalTime{Hour: 7, Minute: 32}, "07:32:00"},
		{toml.LocalTime{Hour: 7, Minute: 32, Nanosecond: 120000000}, "07:32:00.12"},
		{toml.LocalTime{Hour: 7, Minute: 32, Nanosecond: 120000000, Precision: 6}, "07:32:00.120000"},
		{toml.LocalTime{Hour: 23, Minute: 59, Second: 59, Nanosecond: 999999999, Precision: 9}, "23:59:59.999999999"},
	}
	//: each value.
	for _, tc := range tests {
		//: written.
		if got := tc.value.String(); got != tc.text {
			t.Errorf("%+v: String = %q, want %q", tc.value, got, tc.text)
		}
		var back toml.LocalTime
		//: and read back, the precision being what was written.
		if err := back.UnmarshalText([]byte(tc.text)); err != nil || back.String() != tc.text {
			t.Errorf("UnmarshalText(%q) = %+v, %v", tc.text, back, err)
		}
	}
	//: a malformed nanosecond is written as zero, never as other characters.
	if got := (toml.LocalTime{Hour: 1, Nanosecond: -5, Precision: 3}).String(); got != "01:00:00.000" {
		t.Errorf("negative nanosecond: String = %q", got)
	}
	var back toml.LocalTime
	//: a time with an offset is not a local time.
	if err := back.UnmarshalText([]byte("07:32:00Z")); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Errorf("UnmarshalText with an offset = %v", err)
	}
}

// TestLocalDateTimeText writes and reads a date-time, and places it in a zone.
func TestLocalDateTimeText(t *testing.T) {
	t.Parallel()
	dt := toml.LocalDateTime{Year: 1979, Month: 5, Day: 27, Hour: 7, Minute: 32, Nanosecond: 5000, Precision: 6}
	//: date, T, time.
	if dt.String() != "1979-05-27T07:32:00.000005" {
		t.Errorf("String = %q", dt.String())
	}
	//: the T may be a space or a t.
	for _, text := range []string{"1979-05-27T07:32:00.000005", "1979-05-27 07:32:00.000005", "1979-05-27t07:32:00.000005"} {
		var back toml.LocalDateTime
		//: read.
		if err := back.UnmarshalText([]byte(text)); err != nil || back != dt {
			t.Errorf("UnmarshalText(%q) = %+v, %v", text, back, err)
		}
	}
	zone := time.FixedZone("x", 3600)
	//: the wall clock in the zone asked for.
	if got := dt.AsTime(zone); !got.Equal(time.Date(1979, 5, 27, 7, 32, 0, 5000, zone)) {
		t.Errorf("AsTime = %v", got)
	}
}

// TestLocalTypesThroughJSON pins that the three types survive the JSON round
// trip config.Load applies to every decoded document: each is written as its
// text and read back from it.
func TestLocalTypesThroughJSON(t *testing.T) {
	t.Parallel()
	var decoded map[string]any
	//: one of each.
	if err := toml.New().Unmarshal([]byte("d = 2024-02-29\nt = 13:37:00\ndt = 2024-02-29T13:37:00\n"), &decoded); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(decoded)
	//: each as its text.
	if err != nil || string(raw) != `{"d":"2024-02-29","dt":"2024-02-29T13:37:00","t":"13:37:00"}` {
		t.Fatalf("json = %s, %v", raw, err)
	}
	var back struct {
		D  toml.LocalDate     `json:"d"`
		T  toml.LocalTime     `json:"t"`
		DT toml.LocalDateTime `json:"dt"`
	}
	//: and read back by their UnmarshalText.
	if err := json.Unmarshal(raw, &back); err != nil || back.D.Day != 29 || back.T.Minute != 37 || back.DT.Hour != 13 {
		t.Errorf("back = %+v, %v", back, err)
	}
}
