package level_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/level"
)

func TestLevel_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   level.Level
		want string
	}{
		{"below debug", -8, "DEBUG"},
		{"debug boundary", level.Debug, "DEBUG"},
		{"between debug and info", -1, "DEBUG"},
		{"info boundary", level.Info, "INFO"},
		{"between info and warn", 3, "INFO"},
		{"warn boundary", level.Warn, "WARN"},
		{"between warn and error", 7, "WARN"},
		{"error boundary", level.Error, "ERROR"},
		{"above error", 16, "ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.String(); got != tc.want {
				t.Errorf("Level(%d).String() = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLevel_Ordering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		low  level.Level
		high level.Level
	}{
		{"Debug < Info", level.Debug, level.Info},
		{"Info < Warn", level.Info, level.Warn},
		{"Warn < Error", level.Warn, level.Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.low >= tc.high {
				t.Fatalf("ordering broken: %v (%d) must be < %v (%d)", tc.low, tc.low, tc.high, tc.high)
			}
		})
	}
}
