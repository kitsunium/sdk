package logger_test

import (
	"math"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// TestStringValue exercises the StringValue constructor for happy and edge
// cases (empty, multi-byte payload).
func TestStringValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  string
	}{
		{"non-empty round-trip", "alice"},
		{"empty string round-trip", ""},
		{"multi-byte rune round-trip", "héllo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.StringValue(tc.val)
			if v.Kind() != corelogger.KindString || v.String() != tc.val {
				t.Errorf("StringValue(%q): kind=%s str=%q", tc.val, v.Kind(), v.String())
			}
		})
	}
}

// TestInt64Value exercises Int64Value across the signed 64-bit range.
func TestInt64Value(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int64
	}{
		{"min int64", math.MinInt64},
		{"negative one", -1},
		{"zero", 0},
		{"positive one", 1},
		{"max int64", math.MaxInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.Int64Value(tc.val)
			if v.Kind() != corelogger.KindInt64 || v.Int64() != tc.val {
				t.Errorf("Int64Value(%d): kind=%s int64=%d", tc.val, v.Kind(), v.Int64())
			}
		})
	}
}

// TestIntValue confirms the int → int64 widening preserves the payload.
func TestIntValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int
	}{
		{"positive", 42},
		{"zero", 0},
		{"negative", -7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.IntValue(tc.val)
			if v.Kind() != corelogger.KindInt64 || v.Int64() != int64(tc.val) {
				t.Errorf("IntValue(%d): kind=%s int64=%d", tc.val, v.Kind(), v.Int64())
			}
		})
	}
}

// TestUint64Value covers the unsigned 64-bit constructor over its full range.
func TestUint64Value(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"max uint64", math.MaxUint64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.Uint64Value(tc.val)
			if v.Kind() != corelogger.KindUint64 || v.Uint64() != tc.val {
				t.Errorf("Uint64Value(%d): kind=%s uint64=%d", tc.val, v.Kind(), v.Uint64())
			}
		})
	}
}

// TestFloat64Value validates the bit-cast preserves NaN, Inf and finite
// values, and the value goes through the canonical KindFloat64 path.
func TestFloat64Value(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  float64
		nan  bool
	}{
		{"zero", 0, false},
		{"pi", math.Pi, false},
		{"+Inf", math.Inf(1), false},
		{"-Inf", math.Inf(-1), false},
		{"NaN", math.NaN(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.Float64Value(tc.val)
			if v.Kind() != corelogger.KindFloat64 {
				t.Fatalf("Kind = %s, want float64", v.Kind())
			}
			//: NaN is its own non-equal beast — branch on tc.nan.
			if tc.nan {
				if !math.IsNaN(v.Float64()) {
					t.Errorf("NaN round-trip = %v", v.Float64())
				}
				return
			}
			if v.Float64() != tc.val {
				t.Errorf("Float64Value(%v) = %v", tc.val, v.Float64())
			}
		})
	}
}

// TestBoolValue covers the boolean encoding + decoding round-trip.
func TestBoolValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  bool
	}{
		{"true round-trip", true},
		{"false round-trip", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.BoolValue(tc.val)
			if v.Kind() != corelogger.KindBool || v.Bool() != tc.val {
				t.Errorf("BoolValue(%v): kind=%s bool=%v", tc.val, v.Kind(), v.Bool())
			}
		})
	}
}

// TestDurationValue round-trips negative and positive durations through the
// int64-packed bits field.
func TestDurationValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  time.Duration
	}{
		{"negative hour", -time.Hour},
		{"zero", 0},
		{"millisecond", time.Millisecond},
		{"day", 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.DurationValue(tc.val)
			if v.Kind() != corelogger.KindDuration || v.Duration() != tc.val {
				t.Errorf("DurationValue(%v): kind=%s dur=%v", tc.val, v.Kind(), v.Duration())
			}
		})
	}
}

// TestTimeValue covers the time.Time constructor including the zero time and
// a wall-clock instant in UTC.
func TestTimeValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  time.Time
	}{
		{"zero time", time.Time{}},
		{"wall clock UTC", time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.TimeValue(tc.val)
			if v.Kind() != corelogger.KindTime || !v.Time().Equal(tc.val) {
				t.Errorf("TimeValue(%v): kind=%s time=%v", tc.val, v.Kind(), v.Time())
			}
		})
	}
}

// TestGroupValue exercises the nested-attribute payload contract.
func TestGroupValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		attrs []corelogger.AttrValue
	}{
		{"empty group", nil},
		{"single attr", []corelogger.AttrValue{{Key: "id", Value: corelogger.Int64Value(1)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.GroupValue(tc.attrs...)
			if v.Kind() != corelogger.KindGroup {
				t.Fatalf("Kind = %s, want group", v.Kind())
			}
			got := v.Group()
			if len(got) != len(tc.attrs) {
				t.Errorf("len(Group()) = %d, want %d", len(got), len(tc.attrs))
			}
		})
	}
}

// TestAnyValue covers the opaque payload constructor and confirms Any()
// returns the original payload verbatim.
func TestAnyValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
	}{
		{"struct payload round-trip", struct{ N int }{N: 7}},
		{"nil payload is valid", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.AnyValue(tc.val)
			if v.Kind() != corelogger.KindAny || v.Any() != tc.val {
				t.Errorf("AnyValue(%v): kind=%s any=%v", tc.val, v.Kind(), v.Any())
			}
		})
	}
}

// TestNewValue confirms the New-prefixed alias of AnyValue.
func TestNewValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
	}{
		{"struct payload via NewValue", struct{ X int }{X: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.NewValue(tc.val)
			if v.Kind() != corelogger.KindAny || v.Any() != tc.val {
				t.Errorf("NewValue(%v): kind=%s any=%v", tc.val, v.Kind(), v.Any())
			}
		})
	}
}

// TestValue_Kind verifies Kind() returns the discriminator chosen at
// construction time across the full Kind set.
func TestValue_Kind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  corelogger.Value
		want corelogger.Kind
	}{
		{"zero value is KindAny", corelogger.Value{}, corelogger.KindAny},
		{"StringValue → KindString", corelogger.StringValue("x"), corelogger.KindString},
		{"BoolValue → KindBool", corelogger.BoolValue(true), corelogger.KindBool},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.val.Kind(); got != tc.want {
				t.Errorf("Kind() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestValue_String validates the wrong-Kind degradation contract for the
// String() accessor.
func TestValue_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  corelogger.Value
		want string
	}{
		{"KindString returns payload", corelogger.StringValue("hi"), "hi"},
		{"KindInt64 degrades to empty", corelogger.IntValue(7), ""},
		{"KindAny degrades to empty", corelogger.AnyValue(nil), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.val.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestValue_Int64 covers the typed int64 accessor.
func TestValue_Int64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  corelogger.Value
		want int64
	}{
		{"happy path", corelogger.Int64Value(42), 42},
		{"negative round-trip", corelogger.Int64Value(-7), -7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.val.Int64(); got != tc.want {
				t.Errorf("Int64() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestValue_Bits exposes the raw packed storage for diagnostic introspection.
func TestValue_Bits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  corelogger.Value
		want uint64
	}{
		{"Uint64Value packs verbatim", corelogger.Uint64Value(0xDEADBEEF), 0xDEADBEEF},
		{"BoolValue(true) packs as 1", corelogger.BoolValue(true), 1},
		{"BoolValue(false) packs as 0", corelogger.BoolValue(false), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := uint64(tc.val.Bits()); got != tc.want {
				t.Errorf("Bits() = %x, want %x", got, tc.want)
			}
		})
	}
}

// TestValue_Uint64 covers the typed uint64 accessor across edge values.
func TestValue_Uint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  uint64
	}{
		{"zero", 0},
		{"max uint64", math.MaxUint64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.Uint64Value(tc.val)
			if got := v.Uint64(); got != tc.val {
				t.Errorf("Uint64() = %d, want %d", got, tc.val)
			}
		})
	}
}

// TestValue_Float64 confirms the float bit-cast is round-trip safe.
func TestValue_Float64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  float64
	}{
		{"zero", 0},
		{"pi", math.Pi},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.Float64Value(tc.val)
			if got := v.Float64(); got != tc.val {
				t.Errorf("Float64() = %v, want %v", got, tc.val)
			}
		})
	}
}

// TestValue_Bool confirms the bool decoding contract on both values.
func TestValue_Bool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  bool
	}{
		{"true", true},
		{"false", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.BoolValue(tc.val)
			if got := v.Bool(); got != tc.val {
				t.Errorf("Bool() = %v, want %v", got, tc.val)
			}
		})
	}
}

// TestValue_Duration round-trips through the typed Duration accessor.
func TestValue_Duration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  time.Duration
	}{
		{"millisecond", time.Millisecond},
		{"negative hour", -time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := corelogger.DurationValue(tc.val)
			if got := v.Duration(); got != tc.val {
				t.Errorf("Duration() = %v, want %v", got, tc.val)
			}
		})
	}
}

// TestValue_Time covers the wrong-Kind degradation contract for Time().
func TestValue_Time(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		val     corelogger.Value
		wantSet bool
	}{
		{"KindTime returns the stored instant", corelogger.TimeValue(now), true},
		{"non-Time degrades to zero time", corelogger.IntValue(1), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.val.Time()
			if tc.wantSet && !got.Equal(now) {
				t.Errorf("Time() = %v, want %v", got, now)
			}
			if !tc.wantSet && !got.IsZero() {
				t.Errorf("Time() = %v, want zero", got)
			}
		})
	}
}

// TestValue_Group covers the wrong-Kind degradation contract for Group().
func TestValue_Group(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		val     corelogger.Value
		wantLen int
	}{
		{"KindGroup returns the slice", corelogger.GroupValue(corelogger.AttrValue{Key: "k"}), 1},
		{"non-Group degrades to nil", corelogger.IntValue(1), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.val.Group()
			if len(got) != tc.wantLen {
				t.Errorf("len(Group()) = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}

// TestValue_Any confirms only KindAny exposes its payload through Any() and
// typed Kinds return nil from Any().
func TestValue_Any(t *testing.T) {
	t.Parallel()
	payload := struct{ N int }{N: 7}
	tests := []struct {
		name    string
		val     corelogger.Value
		wantNil bool
	}{
		{"KindAny exposes the payload", corelogger.AnyValue(payload), false},
		{"KindInt64 hides the payload", corelogger.IntValue(1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.val.Any()
			if tc.wantNil && got != nil {
				t.Errorf("Any() = %v, want nil", got)
			}
			if !tc.wantNil && got != payload {
				t.Errorf("Any() = %v, want %v", got, payload)
			}
		})
	}
}
