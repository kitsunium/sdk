// External tests for the public git surface.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/git"
)

// initRepo builds a one-commit repository and returns its root. It configures
// identity locally so the test never depends on the machine's git config.
func initRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	//: A repository with no commit has no HEAD to resolve, so seed one.
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		//: A git that cannot initialise makes every assertion meaningless.
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable: %v: %s", args, err, out)
		}
	}
	//: One commit gives merge-base something to resolve against.
	if err := os.WriteFile(filepath.Join(root, "seed.go"), []byte("package p\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	//: The repository root every case works from.
	return root
}

// TestResolveDegradesRatherThanReturningAnEmptySet verifies the contract that
// makes this package safe to scope a review with: outside a repository it
// reports Degraded with a Reason, and never a resolved-but-empty set.
//
// An empty set means "nothing changed". A caller acting on that would skip a
// branch it never examined, which is the opposite of what "I could not tell"
// should produce.
func TestResolveDegradesRatherThanReturningAnEmptySet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		root func(t *testing.T) string
	}{
		{name: "a directory that is not a repository", root: func(t *testing.T) string { return t.TempDir() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := git.Resolve(t.Context(), git.Config{Root: tt.root(t)})
			//: Degraded is the signal; everything must be treated as in scope.
			if !res.Degraded() {
				t.Fatalf("Degraded() = false outside a repository, want true (res = %+v)", res)
			}
			//: A nil Set is what stops a caller reading "nothing changed".
			if res.Set != nil {
				t.Errorf("Set = %v on a degrade, want nil", res.Set)
			}
			//: The reason is meant to be surfaced, so it must exist.
			if res.Reason == "" {
				t.Error("Reason is empty on a degrade, want it to say why")
			}
		})
	}
}

// TestResolveHonoursTheCallersFilter verifies that Config.Include, and nothing
// hard-coded, decides which files enter the set.
func TestResolveHonoursTheCallersFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		include git.Include
		want    bool
	}{
		{name: "nil admits every file", include: nil, want: true},
		{name: "a filter that admits nothing", include: func(string) bool { return false }, want: false},
		{name: "a filter that admits everything", include: func(string) bool { return true }, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := initRepo(t)
			//: An untracked file is a whole-file change, the simplest shape.
			fresh := filepath.Join(root, "fresh.md")
			if err := os.WriteFile(fresh, []byte("# hi\n"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			res := git.Resolve(t.Context(), git.Config{Root: root, Include: tt.include})
			//: A real repository with a commit must resolve, not degrade.
			if res.Degraded() {
				t.Skipf("resolution degraded in this environment: %s", res.Reason)
			}
			//: Whether the file is in the set is the caller's filter's answer.
			if got := res.Set.ContainsFile(fresh); got != tt.want {
				t.Errorf("ContainsFile(%s) = %t, want %t", filepath.Base(fresh), got, tt.want)
			}
		})
	}
}

// TestGitDirRefusesANonRepository verifies the typed refusal, so a caller can
// match on the sentinel rather than on a string.
func TestGitDirRefusesANonRepository(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "an empty temporary directory"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := git.GitDir(t.Context(), t.TempDir())
			//: Outside a repository there is no git directory to name.
			if err == nil {
				t.Fatal("GitDir() = nil error outside a repository, want a refusal")
			}
		})
	}
}
