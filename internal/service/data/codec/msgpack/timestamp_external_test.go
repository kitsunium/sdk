package msgpack_test

import (
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// TestTimestampRoundTrip keeps the instant of times in every form —
// before 1970, past 2106 and 2514, with and without nanoseconds, from a
// fixed zone — and decodes each in UTC.
func TestTimestampRoundTrip(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("UTC+5:30", int((5*time.Hour+30*time.Minute)/time.Second))
	tests := []struct {
		name string
		in   time.Time
	}{
		{"epoch", time.Unix(0, 0)},
		{"32-bit", time.Unix(1_700_000_000, 0)},
		{"64-bit with nanoseconds", time.Unix(1_700_000_000, 999_999_999)},
		{"past 2106", time.Unix(math.MaxUint32+1, 1)},
		{"past 2514, 96-bit", time.Unix(1<<34, 0)},
		{"before 1970", time.Date(1969, time.July, 20, 20, 17, 40, 5, time.UTC)},
		{"year 1", time.Date(1, time.January, 1, 0, 0, 0, 1, time.UTC)},
		{"fixed zone", time.Date(2024, time.March, 1, 12, 0, 0, 0, zone)},
		{"monotonic reading dropped", time.Now()},
	}
	c := msgpack.New()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got time.Time
			if err := c.Unmarshal(mustMarshal(t, tc.in), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.in) || got.Location() != time.UTC {
				t.Fatalf("got %v (%v), want the instant %v in UTC", got, got.Location(), tc.in)
			}
		})
	}
}

// TestTimestampZeroIsExact round-trips the zero time to exactly time.Time{},
// so a struct holding one compares equal after a round trip.
func TestTimestampZeroIsExact(t *testing.T) {
	t.Parallel()
	var got time.Time
	if err := msgpack.New().Unmarshal(mustMarshal(t, time.Time{}), &got); err != nil {
		t.Fatal(err)
	}
	if got != (time.Time{}) {
		t.Fatalf("got %#v", got)
	}
}

// TestTimestampFromText reads an RFC 3339 string into a time, in UTC.
func TestTimestampFromText(t *testing.T) {
	t.Parallel()
	var got time.Time
	if err := msgpack.New().Unmarshal(mustMarshal(t, "2024-01-15T10:30:00+01:00"), &got); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2024, time.January, 15, 9, 30, 0, 0, time.UTC); got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
