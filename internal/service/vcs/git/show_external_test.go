// External tests for show.go - git package.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/vcs/git"
)

// showTestRepo initialises a real repository with one committed file and
// returns the repo root plus the commit SHA.
func showTestRepo(t *testing.T) (root, sha string) {
	t.Helper()
	root = t.TempDir()
	//: A real git history is the contract under test — no stubs.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
	} {
		full := append([]string{"-C", root}, args...)
		//: Fixture setup must fail loudly, never silently skip.
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("hello blob\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, args := range [][]string{
		{"add", "f.txt"},
		{"commit", "-q", "-m", "base"},
	} {
		full := append([]string{"-C", root}, args...)
		//: Fixture setup must fail loudly, never silently skip.
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	//: The commit SHA anchors every ShowFile call below.
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	//: Return the initialised repo and its single commit.
	return root, strings.TrimSpace(string(out))
}

// TestShowFile pins the blob read contract: a committed path returns its
// content, an absent path returns an error (never an empty success).
func TestShowFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		relPath string
		want    string
		wantErr bool
	}{
		{name: "committed file returns its content", relPath: "f.txt", want: "hello blob"},
		{name: "absent path returns an error", relPath: "missing.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, sha := showTestRepo(t)

			got, err := git.ShowFile(t.Context(), root, sha, tt.relPath)

			//: Absence must surface as an error, distinguishable from empty.
			if (err != nil) != tt.wantErr {
				t.Fatalf("ShowFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ShowFile() = %q, want %q", got, tt.want)
			}
		})
	}
}
