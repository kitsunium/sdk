package logger

import "testing"

// Test_kindLabels pins the lookup table indexed by Kind so future re-orderings
// trigger a regression rather than silent label drift.
func Test_kindLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind Kind
		want string
	}{
		{"KindAny label", KindAny, "any"},
		{"KindGroup label", KindGroup, "group"},
		{"KindString label", KindString, "string"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kindLabels[tc.kind]; got != tc.want {
				t.Errorf("kindLabels[%d] = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// Test_unknownKindLabel pins the sentinel returned for out-of-range Kinds.
func Test_unknownKindLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"unknownKindLabel is unknown", "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if unknownKindLabel != tc.want {
				t.Errorf("unknownKindLabel = %q, want %q", unknownKindLabel, tc.want)
			}
		})
	}
}
