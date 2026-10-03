package encoder

import (
	"testing"
	"time"
)

// timestampSweepSteps is how many instants each zone contributes to the
// differential sweep. The step below is deliberately not a round number of
// seconds so the walk crosses minute, hour, day, month and year boundaries at
// irregular offsets rather than landing on them.
const timestampSweepSteps int = 100_000

// timestampSweepStep advances the sweep. 997 ms is coprime with 1000, so every
// millisecond value in [0, 999] is visited and the ".000" verb's padding is
// exercised at 007 and 070 as well as 700.
const timestampSweepStep time.Duration = 997 * time.Millisecond

// TestAppendTimestampMatchesAppendFormat is the differential test that
// licenses appendTimestamp. It replaced time.Time.AppendFormat on the hot path
// because a profile put 34.6 % of a whole emit inside it (BENCH.md §5.2), and
// that substitution is only legitimate while the two produce identical bytes.
//
// The sweep runs four zones — UTC, a positive whole-hour offset, a negative
// half-hour offset, and a 45-minute offset that exists in the real tz database
// (Nepal) — over a hundred thousand instants each, and compares the renderings
// byte for byte.
func TestAppendTimestampMatchesAppendFormat(t *testing.T) {
	t.Parallel()
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("plus-two", 2*3600),
		time.FixedZone("minus-five-thirty", -(5*3600 + 30*60)),
		time.FixedZone("plus-forty-five", 45*60),
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 6*int(time.Millisecond), time.UTC)
	for _, zone := range zones {
		for i := range timestampSweepSteps {
			ts := base.Add(time.Duration(i) * timestampSweepStep).In(zone)
			want := string(ts.AppendFormat(nil, timestampLayout))
			got := string(appendTimestamp(nil, ts))
			//: byte equality is the whole contract — a "close enough"
			//: timestamp silently corrupts every line the SDK has emitted.
			if got != want {
				t.Fatalf("zone %v step %d: appendTimestamp = %q, want %q", zone, i, got, want)
			}
		}
	}
}

// TestAppendTimestampEdgeInstants pins the instants a sweep is unlikely to
// visit: the zero Time, the four-digit year boundaries, and the years on
// either side of them that MUST fall back to the stdlib formatter because the
// "2006" verb renders them at a different width.
func TestAppendTimestampEdgeInstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ts   time.Time
	}{
		{"zero value", time.Time{}},
		{"year one", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"first renderable year", time.Date(timestampMinYear, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"last renderable year", time.Date(timestampMaxYear, 12, 31, 23, 59, 59, 999*int(time.Millisecond), time.UTC)},
		{"year past the four-digit window", time.Date(timestampMaxYear+1, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"year before the four-digit window", time.Date(timestampMinYear-1, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"leap day", time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC)},
		{"millisecond needing two pad zeros", time.Date(2026, 3, 4, 5, 6, 7, 7*int(time.Millisecond), time.UTC)},
		{"nanoseconds truncate rather than round", time.Date(2026, 3, 4, 5, 6, 7, 999_999_999, time.UTC)},
		{"offset carrying seconds", time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("lmt", 3*3600+17*60+32))},
		{"negative offset carrying seconds", time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("lmt", -(3*3600+17*60+32)))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := string(tc.ts.AppendFormat(nil, timestampLayout))
			//: the fallback path is part of the contract, so an out-of-window
			//: year must still come back byte-identical.
			if got := string(appendTimestamp(nil, tc.ts)); got != want {
				t.Errorf("appendTimestamp = %q, want %q", got, want)
			}
		})
	}
}

// TestTimestampLayoutIsTheRenderedShape pins the layout string itself. Both
// encoders now share this one constant — the JSON encoder used to carry an
// identical copy, and appendTimestamp renders exactly this shape and no other.
// So changing the constant without changing appendTimestamp would silently
// make the two disagree; this asserts the constant, and the differential tests
// above assert the renderer against it.
func TestTimestampLayoutIsTheRenderedShape(t *testing.T) {
	t.Parallel()
	const want string = "2006-01-02T15:04:05.000Z07:00"
	if timestampLayout != want {
		t.Errorf("timestampLayout = %q, want %q — appendTimestamp renders only this shape", timestampLayout, want)
	}
}
