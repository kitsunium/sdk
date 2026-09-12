// Package git white-box tests the resolution helpers.
package git

import "testing"

// Test_addDiff_propagatesGitError verifies a git failure (here: running diff in
// a non-repo directory) is returned, not swallowed — the mechanism that lets
// Resolve degrade loudly to a full scan instead of surfacing a silent empty diff.
func Test_addDiff_propagatesGitError(t *testing.T) {
	t.Parallel()
	//: `git diff` in a non-repo dir fails; addDiff must surface that error.
	if err := addDiff(t.Context(), t.TempDir(), NewChangedSetValue(t.TempDir()), nil, "HEAD"); err == nil {
		t.Error("addDiff should propagate a git failure, got nil")
	}
}

// Test_repoTopLevel_nonRepo verifies a directory outside any repository reports
// "no repo" rather than erroring.
func Test_repoTopLevel_nonRepo(t *testing.T) {
	t.Parallel()
	_, ok := repoTopLevel(t.Context(), t.TempDir())
	//: A non-repo hint must signal "no repo" so Resolve falls back.
	if ok {
		t.Error("repoTopLevel on a non-repo dir reported ok=true")
	}
}

// Test_isShallow_nonRepo verifies the shallow probe defaults to false outside a
// repository.
func Test_isShallow_nonRepo(t *testing.T) {
	t.Parallel()
	//: A failed probe must default to "not shallow".
	if isShallow(t.Context(), t.TempDir()) {
		t.Error("isShallow on a non-repo dir returned true")
	}
}

// Test_fullFallback verifies the fallback constructor sets the documented
// full-scan contract.
func Test_fullFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "no git", reason: "not a git repository — full scan"},
		{name: "shallow", reason: "shallow clone — full scan"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := fullFallback(tt.reason)
			//: The full-scan contract: flag set, reason carried, no set.
			if !res.FullFallback || res.Reason != tt.reason || res.Set != nil {
				t.Errorf("fullFallback(%q) = %+v, want full-scan contract", tt.reason, res)
			}
		})
	}
}
