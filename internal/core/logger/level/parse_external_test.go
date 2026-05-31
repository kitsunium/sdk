package level_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    level.Level
		wantErr bool
	}{
		{"debug", "debug", level.Debug, false},
		{"info", "info", level.Info, false},
		{"warn", "warn", level.Warn, false},
		{"error", "error", level.Error, false},
		{"uppercase info", "INFO", level.Info, false},
		{"mixed-case warn with spaces", "  Warn ", level.Warn, false},
		{"round-trip from String", "debug", level.Debug, false},
		{"empty", "", level.Level(0), true},
		{"unknown name", "trace", level.Level(0), true},
		{"numeric", "4", level.Level(0), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := level.ParseLevel(tc.in)
			//: assert the error arm matches the table expectation.
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLevel(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			//: on the error path the sentinel must be level.LevelUnknown.
			if tc.wantErr {
				//: identity match keeps errors.Is / HasCode contracts intact.
				if !errors.Is(err, level.LevelUnknown) {
					t.Fatalf("ParseLevel(%q) err = %v, want level.LevelUnknown", tc.in, err)
				}
				return
			}
			//: on success the parsed Level must equal the expectation.
			if got != tc.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseLevel_RoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   level.Level
	}{
		{"Debug", level.Debug},
		{"Info", level.Info},
		{"Warn", level.Warn},
		{"Error", level.Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: String() uppercases; ParseLevel lowercases — the pair round-trips.
			got, err := level.ParseLevel(tc.in.String())
			//: the canonical label must parse back without error.
			if err != nil {
				t.Fatalf("ParseLevel(%q) unexpected err = %v", tc.in.String(), err)
			}
			//: the parsed Level must equal the original constant.
			if got != tc.in {
				t.Fatalf("round-trip %v -> %q -> %v mismatch", tc.in, tc.in.String(), got)
			}
		})
	}
}
