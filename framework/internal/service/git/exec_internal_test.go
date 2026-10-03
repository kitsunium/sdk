// Package git white-box tests the git exec helpers.
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Test_runGitOutput verifies success returns trimmed stdout and failure folds
// stderr into a non-nil error with an empty string result.
func Test_runGitOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "version succeeds", args: []string{"version"}, wantErr: false},
		{name: "bogus subcommand fails", args: []string{"definitely-not-a-git-command"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := runGitOutput(t.Context(), t.TempDir(), tt.args...)
			//: The error expectation is the contract.
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			//: A failed call must yield an empty result, not partial output.
			if tt.wantErr && out != "" {
				t.Errorf("failed call returned %q, want empty", out)
			}
			//: A successful `git version` prints a non-empty banner.
			if !tt.wantErr && out == "" {
				t.Error("successful call returned empty output")
			}
		})
	}
}

// Test_gitProbe verifies the boolean probe mirrors command success.
func Test_gitProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "version is true", args: []string{"version"}, want: true},
		{name: "bogus is false", args: []string{"definitely-not-a-git-command"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: The probe result must mirror command success.
			if got := gitProbe(t.Context(), t.TempDir(), tt.args...); got != tt.want {
				t.Errorf("gitProbe(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// plantHostileRepo builds a git repository whose .git/config points the given
// command-execution keys at a script that records every invocation.
//
// Parameters:
//   - t: the owning test, used for the temp dir and fatal reporting.
//   - keys: git config keys to point at the recording script.
//
// Returns:
//   - repo: path of the repository.
//   - evidence: path of the file the script appends to on each execution.
func plantHostileRepo(t *testing.T, keys ...string) (repo, evidence string) {
	t.Helper()
	dir := t.TempDir()
	repo = filepath.Join(dir, "repo")
	evidence = filepath.Join(dir, "evidence")

	//: A repository is required before any config can be planted.
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	script := filepath.Join(dir, "payload.sh")
	body := "#!/bin/sh\necho executed >> " + evidence + "\nexit 1\n"
	//: Without the payload there is nothing to detect.
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("writing payload: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		out, err := cmd.CombinedOutput()
		//: A broken fixture would make the assertions below meaningless.
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", ".")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "base")

	//: Seed a tracked file so diff has something to report on.
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatalf("seeding file: %v", err)
	}
	run("add", "f.txt")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "seed")
	//: Dirty the worktree so the diff is non-empty.
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatalf("dirtying file: %v", err)
	}

	//: Point each requested execution key at the payload.
	for _, key := range keys {
		run("config", key, script)
	}
	return repo, evidence
}

// executionCount reports how many times the planted payload ran.
//
// Parameters:
//   - t: the owning test.
//   - evidence: path written by the payload.
//
// Returns:
//   - n: number of recorded executions.
func executionCount(t *testing.T, evidence string) (n int) {
	t.Helper()
	data, err := os.ReadFile(evidence)
	//: A missing evidence file means the payload never ran.
	if err != nil {
		return 0
	}
	//: Return the computed result to the caller.
	return strings.Count(string(data), "executed")
}

// TestRunGitOutputRefusesRepositoryControlledExecution is the regression test
// for a confirmed local code-execution vector.
//
// The MCP daemon runs git against whatever repository the user opened, and
// .git/config travels with a clone. Git will happily execute a command named by
// that config on operations the user believes are read-only. Against an
// unhardened invocation, a payload planted in core.fsmonitor and diff.external
// executed 5 times during a plain `diff` plus `status`.
//
// Unix-only: the payload is a /bin/sh script.
func TestRunGitOutputRefusesRepositoryControlledExecution(t *testing.T) {
	t.Parallel()

	//: The payload is a POSIX shell script.
	if runtime.GOOS == "windows" {
		t.Log("skipping: the planted payload is a /bin/sh script")
		return
	}

	tests := []struct {
		name string
		keys []string
		args []string
	}{
		{name: "fsmonitor on diff", keys: []string{"core.fsmonitor"}, args: []string{"diff", "-U0", "-M", "-C"}},
		{name: "fsmonitor on ls-files", keys: []string{"core.fsmonitor"}, args: []string{"ls-files", "--others", "--exclude-standard", "-z"}},
		{name: "diff.external on diff", keys: []string{"diff.external"}, args: []string{"diff", "-U0", "-M", "-C"}},
		{name: "diff.external on name-status", keys: []string{"diff.external"}, args: []string{"diff", "--name-status", "-z", "-M", "-C"}},
		{name: "both keys on diff", keys: []string{"core.fsmonitor", "diff.external"}, args: []string{"diff", "-U0", "-M", "-C"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo, evidence := plantHostileRepo(t, tt.keys...)

			//: The call must succeed: hardening that breaks git is not hardening.
			out, err := runGitOutput(t.Context(), repo, tt.args...)
			if err != nil {
				t.Fatalf("runGitOutput(%v) failed: %v", tt.args, err)
			}

			//: No repository-controlled command may have run.
			if n := executionCount(t, evidence); n != 0 {
				t.Errorf("planted payload executed %d time(s) via %v", n, tt.keys)
			}
			//: A hardened diff must still produce real output, not an empty string.
			if tt.args[0] == "diff" && out == "" {
				t.Errorf("hardened %v returned no output; the guard broke the command", tt.args)
			}
		})
	}
}

// TestExtDiffGuard pins where --no-ext-diff is injected. It cannot be appended
// to every invocation: rev-parse, ls-files and merge-base reject the flag, so
// blanket injection would break them.
func TestExtDiffGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "diff is guarded", args: []string{"diff", "-U0"}, want: []string{"diff", "--no-ext-diff", "-U0"}},
		{name: "show is guarded", args: []string{"show", "HEAD:f"}, want: []string{"show", "--no-ext-diff", "HEAD:f"}},
		{name: "log is guarded", args: []string{"log", "-1"}, want: []string{"log", "--no-ext-diff", "-1"}},
		{name: "rev-parse is untouched", args: []string{"rev-parse", "HEAD"}, want: []string{"rev-parse", "HEAD"}},
		{name: "ls-files is untouched", args: []string{"ls-files", "-z"}, want: []string{"ls-files", "-z"}},
		{name: "merge-base is untouched", args: []string{"merge-base", "a", "b"}, want: []string{"merge-base", "a", "b"}},
		{name: "empty is untouched", args: []string{}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extDiffGuard(tt.args)
			//: The flag must land immediately after the subcommand, or not at all.
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("extDiffGuard(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
