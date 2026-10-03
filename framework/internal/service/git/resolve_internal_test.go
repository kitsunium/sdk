// Package git white-box tests the resolution helpers.
package git

import (
	"path/filepath"
	"testing"
)

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
	_, _, ok := repoTopLevel(t.Context(), t.TempDir())
	//: A non-repo hint must signal "no repo" so Resolve falls back.
	if ok {
		t.Error("repoTopLevel on a non-repo dir reported ok=true")
	}
}

// Test_shallowState_nonRepo pins the half of the shallow probe that used to be
// invisible: outside a repository the probe cannot run, and "it did not run" is
// reported as UNKNOWN rather than as "not shallow".
//
// The two used to be the same value. A git that does not know
// --is-shallow-repository, or one that is not there, made the probe answer
// false, and Resolve read that as full history and scoped the diff against a
// merge-base it had no history for.
func Test_shallowState_nonRepo(t *testing.T) {
	t.Parallel()
	shallow, known := shallowState(t.Context(), t.TempDir())
	//: A probe that could not run tells us nothing about the history.
	if known {
		t.Errorf("shallowState on a non-repo dir = (%v, known=true), want known=false", shallow)
	}
	//: And "unknown" must not smuggle a positive answer through either.
	if shallow {
		t.Error("shallowState reported shallow=true without knowing")
	}
}

// Test_spelledTopLevel covers the one syscall this package makes on a path:
// deriving the caller's own spelling of a repository top level git reported
// canonically. The empty answer is the ordinary one, and every row that
// produces a non-empty alias has to survive being joined back onto a path.
func Test_spelledTopLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: build lays out the tree and returns (hint, canonical).
		build func(t *testing.T, base string) (hint, canonical string)
		//: want is resolved against the base directory when nonEmpty is set.
		want     func(base string) string
		nonEmpty bool
	}
	tests := []tc{
		{
			name: "a hint that is already the canonical root has no alias",
			build: func(t *testing.T, base string) (string, string) {
				t.Helper()
				//: The caller already speaks git's spelling.
				return base, base
			},
			want:     func(string) string { return "" },
			nonEmpty: false,
		},
		{
			name: "a hint inside the canonical root has no alias",
			build: func(t *testing.T, base string) (string, string) {
				t.Helper()
				sub := filepath.Join(base, "sub")
				mkdirT(t, sub)
				//: Lexically under the root — nothing to rewrite.
				return sub, base
			},
			want:     func(string) string { return "" },
			nonEmpty: false,
		},
		{
			name: "a hint that IS a link to the root aliases the link itself",
			build: func(t *testing.T, base string) (string, string) {
				t.Helper()
				real := filepath.Join(base, "real")
				mkdirT(t, real)
				link := filepath.Join(base, "link")
				symlinkT(t, real, link)
				//: rel resolves to ".", so the whole hint is the alias.
				return link, real
			},
			want:     func(base string) string { return filepath.Join(base, "link") },
			nonEmpty: true,
		},
		{
			name: "a hint below a linked root aliases the link, tail stripped",
			build: func(t *testing.T, base string) (string, string) {
				t.Helper()
				real := filepath.Join(base, "real")
				mkdirT(t, filepath.Join(real, "a", "b"))
				link := filepath.Join(base, "link")
				symlinkT(t, real, link)
				//: The caller points two directories deep through the link.
				return filepath.Join(link, "a", "b"), real
			},
			want:     func(base string) string { return filepath.Join(base, "link") },
			nonEmpty: true,
		},
		{
			name: "a hint whose resolved path is outside the root has no alias",
			build: func(t *testing.T, base string) (string, string) {
				t.Helper()
				real := filepath.Join(base, "real")
				mkdirT(t, real)
				other := filepath.Join(base, "other")
				mkdirT(t, other)
				//: Resolves outside the canonical root: no prefix describes it.
				return other, real
			},
			want:     func(string) string { return "" },
			nonEmpty: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: canonical stands for what git reports, a path with no link in it —
		//: and a temporary directory may sit under one (macOS: /var is a link
		//: to /private/var), so the tree is laid out under the resolved base.
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("resolving the temporary directory: %v", err)
		}
		hint, canonical := c.build(t, base)
		got, ok := spelledTopLevel(hint, canonical)
		want := c.want(base)
		//: An alias that is wrong is worse than none: it would record every
		//: entry under a path no caller will ever query.
		if got != want || ok != c.nonEmpty {
			t.Fatalf("spelledTopLevel(%q, %q) = (%q, %v), want (%q, %v)", hint, canonical, got, ok, want, c.nonEmpty)
		}
		//: An alias has to be a prefix a recorded path can actually carry.
		if c.nonEmpty && !filepath.IsAbs(got) {
			t.Fatalf("spelledTopLevel returned a non-absolute alias %q", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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
