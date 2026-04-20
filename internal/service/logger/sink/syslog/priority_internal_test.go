package syslog

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func Test_severityFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		lv   level.Level
		want int
	}{
		{"error maps to severityError", level.Error, severityError},
		{"warn maps to severityWarning", level.Warn, severityWarning},
		{"info maps to severityInfo", level.Info, severityInfo},
		{"debug maps to severityDebug", level.Debug, severityDebug},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := severityFor(tc.lv); got != tc.want {
				t.Errorf("severityFor(%v) = %d, want %d", tc.lv, got, tc.want)
			}
		})
	}
}

func Test_priorityFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		lv   level.Level
		want int
	}{
		{"info → facility 1 + severity 6 = 14", level.Info, 14},
		{"error → facility 1 + severity 3 = 11", level.Error, 11},
		{"debug → facility 1 + severity 7 = 15", level.Debug, 15},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := priorityFor(tc.lv); got != tc.want {
				t.Errorf("priorityFor(%v) = %d, want %d", tc.lv, got, tc.want)
			}
		})
	}
}

func Test_facilityShift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"facilityShift is 8 per RFC5424", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if facilityShift != tc.want {
				t.Errorf("facilityShift = %d, want %d", facilityShift, tc.want)
			}
		})
	}
}
