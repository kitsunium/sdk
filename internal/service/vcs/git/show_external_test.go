// External tests for show.go - git package.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
	//: An empty file that IS committed is the other half of the distinction:
	//: it must read back as a success, not as the absence beside it.
	if err := os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	for _, args := range [][]string{
		{"add", "f.txt", "empty.txt"},
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

	type tc struct {
		name    string
		relPath string
		//: badSHA asks for a commit that does not resolve instead of the real one.
		badSHA  bool
		want    string
		wantErr bool
		//: wantCode is the sentinel the refusal must carry, 0 when there is none.
		wantCode errs.Code
	}
	tests := []tc{
		//: The trailing newline is the FILE's, and the source implementation's
		//: trim removed it. This expectation changed with the behaviour: a
		//: blob comes back verbatim now, because trimming a file silently
		//: rewrites its content and makes a whitespace-only file read as empty.
		{name: "committed file returns its content", relPath: "f.txt", want: "hello blob\n"},
		{
			name:     "absent path is PATH_ABSENT, not a generic failure",
			relPath:  "missing.txt",
			wantErr:  true,
			wantCode: corevcs.CodePathAbsent,
		},
		{
			name:     "an empty committed file is a successful read of nothing",
			relPath:  "empty.txt",
			want:     "",
			wantErr:  false,
			wantCode: 0,
		},
		{
			name:     "a commit that does not resolve stays COMMAND_FAILED",
			relPath:  "f.txt",
			badSHA:   true,
			wantErr:  true,
			wantCode: corevcs.CodeCommandFailed,
		},
		{
			name:     "an absent path under a commit that does not resolve stays COMMAND_FAILED",
			relPath:  "missing.txt",
			badSHA:   true,
			wantErr:  true,
			wantCode: corevcs.CodeCommandFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root, sha := showTestRepo(t)
		//: An unreachable commit is not a statement about any path in it.
		if c.badSHA {
			sha = "0000000000000000000000000000000000000000"
		}

		got, err := git.ShowFile(t.Context(), root, sha, c.relPath)

		//: Absence must surface as an error, distinguishable from empty.
		if (err != nil) != c.wantErr {
			t.Fatalf("ShowFile() error = %v, wantErr %v", err, c.wantErr)
		}
		//: Which refusal it is decides what a caller does next: retry another
		//: path, or stop because the repository could not be read at all.
		if c.wantErr && !errs.HasCode(err, c.wantCode) {
			t.Fatalf("ShowFile() error = %v, want code %v", err, c.wantCode)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ShowFile() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
