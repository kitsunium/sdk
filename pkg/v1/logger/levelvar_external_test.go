package logger_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    logger.Level
		wantErr bool
	}{
		{"debug", "debug", logger.LevelDebug, false},
		{"info", "info", logger.LevelInfo, false},
		{"warn", "warn", logger.LevelWarn, false},
		{"error", "error", logger.LevelError, false},
		{"uppercase", "ERROR", logger.LevelError, false},
		{"unknown", "verbose", logger.Level(0), true},
		{"empty", "", logger.Level(0), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := logger.ParseLevel(tc.in)
			//: assert the error arm matches the table expectation.
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseLevel(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			//: a miss must surface a non-nil, matchable error and stop here.
			if tc.wantErr {
				//: the sentinel must satisfy errors.Is for HasCode-style routing.
				if err == nil || errors.Is(err, nil) {
					t.Fatalf("ParseLevel(%q) expected a matchable error", tc.in)
				}
				return
			}
			//: success path: the parsed Level must equal the expectation.
			if got != tc.want {
				t.Fatalf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewLevelVar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial logger.Level
		set     logger.Level
	}{
		{"seed info set error", logger.LevelInfo, logger.LevelError},
		{"seed error set debug", logger.LevelError, logger.LevelDebug},
		{"seed warn set warn", logger.LevelWarn, logger.LevelWarn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lv := logger.NewLevelVar(tc.initial)
			//: the holder must report the seed before any Set.
			if got := lv.Level(); got != tc.initial {
				t.Fatalf("NewLevelVar(%v).Level() = %v, want %v", tc.initial, got, tc.initial)
			}
			lv.Set(tc.set)
			//: a Set on the alias type must be observable through Level.
			if got := lv.Level(); got != tc.set {
				t.Fatalf("after Set(%v), Level() = %v, want %v", tc.set, got, tc.set)
			}
		})
	}
}

func TestNewLevelVar_SatisfiesLeveler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		seed logger.Level
	}{
		{"warn seed", logger.LevelWarn},
		{"error seed", logger.LevelError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: the constructor result must be usable wherever a Leveler is required.
			lvl := logger.Leveler(logger.NewLevelVar(tc.seed))
			if got := lvl.Level(); got != tc.seed {
				t.Fatalf("Leveler.Level() = %v, want %v", got, tc.seed)
			}
		})
	}
}
