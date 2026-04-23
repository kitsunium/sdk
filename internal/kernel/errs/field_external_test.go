package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"plain", "k", "v", "v"},
		{"empty", "k", "", ""},
		{"utf8", "k", "éàü", "éàü"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := errs.String(tc.key, tc.val)
			if f.Key() != tc.key {
				t.Errorf("Key() = %q, want %q", f.Key(), tc.key)
			}
			if f.StringValue() != tc.want {
				t.Errorf("StringValue() = %q, want %q", f.StringValue(), tc.want)
			}
		})
	}
}

func TestInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int
		want string
	}{
		{"zero", 0, "0"},
		{"positive", 42, "42"},
		{"negative", -17, "-17"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.Int("k", tc.val).StringValue()
			if got != tc.want {
				t.Errorf("StringValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInt64 covers the explicit int64 constructor added alongside Int.
func TestInt64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  int64
		want string
	}{
		{"zero", 0, "0"},
		{"max int64", 9_223_372_036_854_775_807, "9223372036854775807"},
		{"min int64", -9_223_372_036_854_775_808, "-9223372036854775808"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.Int64("k", tc.val).StringValue()
			if got != tc.want {
				t.Errorf("StringValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  bool
		want string
	}{
		{"true", true, "true"},
		{"false", false, "false"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.Bool("k", tc.val).StringValue()
			if got != tc.want {
				t.Errorf("StringValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  float64
		want string
	}{
		{"zero", 0, "0"},
		{"half", 0.5, "0.5"},
		{"integer", 42.0, "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.Float("k", tc.val).StringValue()
			if got != tc.want {
				t.Errorf("StringValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewFieldValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"delegates to String"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := errs.NewFieldValue("k", "v")
			if f.Key() != "k" || f.StringValue() != "v" {
				t.Errorf("NewFieldValue => %s=%s", f.Key(), f.StringValue())
			}
		})
	}
}

func TestFieldValue_Key(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
	}{
		{"simple", "k"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := errs.String(tc.key, "v")
			if f.Key() != tc.key {
				t.Errorf("Key = %q", f.Key())
			}
		})
	}
}

func TestFieldValue_StringValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		make func() errs.FieldValue
		want string
	}{
		{"string", func() errs.FieldValue { return errs.String("k", "s") }, "s"},
		{"int", func() errs.FieldValue { return errs.Int("k", 42) }, "42"},
		{"bool", func() errs.FieldValue { return errs.Bool("k", true) }, "true"},
		{"float", func() errs.FieldValue { return errs.Float("k", 0.5) }, "0.5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := tc.make()
			if f.StringValue() != tc.want {
				t.Errorf("StringValue = %q, want %q", f.StringValue(), tc.want)
			}
		})
	}
}
