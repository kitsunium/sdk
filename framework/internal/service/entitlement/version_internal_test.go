package entitlement

import "testing"

// Test_normaliseTag pins that both spellings of a version reach the same
// comparison. The build stamp writes "1.5.14" while the roster and every tag
// use "v1.5.14"; semver.IsValid rejects the first, so a missing prefix would
// silently make every comparison fail open and disable the floor entirely.
func Test_normaliseTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
		reason  string
	}{
		{name: "a bare version gains the prefix", version: "1.5.14", want: "v1.5.14", reason: "the build stamp writes the bare form"},
		{name: "a prefixed version is untouched", version: "v1.5.14", want: "v1.5.14", reason: "tags and rosters already carry it"},
		{name: "surrounding whitespace is trimmed", version: " v1.5.14\n", want: "v1.5.14", reason: "a stamp file may carry a trailing newline"},
		{name: "an empty version stays empty", version: "", want: "", reason: "adding a prefix to nothing would fabricate \"v\""},
		{name: "whitespace only stays empty", version: "   ", want: "", reason: "same reasoning as the empty case"},
		{name: "a prerelease keeps its suffix", version: "1.5.14-rc.1", want: "v1.5.14-rc.1", reason: "SemVer ordering depends on it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := normaliseTag(tt.version); got != tt.want {
				t.Errorf("normaliseTag(%q) = %q, want %q (%s)", tt.version, got, tt.want, tt.reason)
			}
		})
	}
}
