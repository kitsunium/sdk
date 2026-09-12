// Package git internal tests for the per-root git-dir memo: a cached root
// must answer without re-running git, and only string entries are trusted.
package git

import (
	"path/filepath"
	"testing"
)

// TestGitDir_cache verifies the memo contract from the inside: a
// pre-seeded cache entry is served verbatim (no subprocess — the root is not
// even a repository), while a foreign-typed entry is distrusted and falls
// through to a real resolution, which fails outside a repo.
func TestGitDir_cache(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: seed stores a synthetic cache entry for the root; nil skips seeding.
		seed    func(absRoot string)
		want    string
		wantErr bool
	}{
		{
			name: "seeded string entry served without git",
			seed: func(absRoot string) {
				//: The fake git dir proves the answer came from the memo:
				//: the root is NOT a repository, so a real resolution would fail.
				gitDirCache.Store(absRoot, "/memo/fake-gitdir")
			},
			want:    "/memo/fake-gitdir",
			wantErr: false,
		},
		{
			name: "foreign-typed entry falls through to a real resolution",
			seed: func(absRoot string) {
				//: A non-string entry must be distrusted, not type-panicked.
				gitDirCache.Store(absRoot, 42)
			},
			want:    "",
			wantErr: true,
		},
		{
			name:    "unseeded non-repo root errors",
			seed:    nil,
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			abs, absErr := filepath.Abs(root)
			//: The cache key is the cleaned absolute root.
			if absErr != nil {
				t.Fatalf("abs(%s): %v", root, absErr)
			}
			abs = filepath.Clean(abs)
			//: Seed the memo when the row asks for it.
			if tt.seed != nil {
				tt.seed(abs)
			}

			got, err := GitDir(t.Context(), root)
			//: The error expectation must match the row.
			if (err != nil) != tt.wantErr {
				t.Fatalf("GitDir(%s) error = %v, wantErr %v", root, err, tt.wantErr)
			}
			//: The resolved value must match the seeded (or empty) expectation.
			if got != tt.want {
				t.Errorf("GitDir(%s) = %q, want %q", root, got, tt.want)
			}
		})
	}
}
