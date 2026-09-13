//go:build !windows

// Package git_test drives the shallow probe against a git that will not answer.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// shimScript is a git that forwards every invocation to the real binary,
// except the shallow probe, where it does whatever the row under test needs.
// The two placeholders are substituted textually rather than formatted, so a
// shell fragment carrying a percent sign stays what it is.
const shimScript string = `#!/bin/sh
for a in "$@"; do
  if [ "$a" = "--is-shallow-repository" ]; then
    __BEHAVIOUR__
  fi
done
exec "__GIT__" "$@"
`

// shimBehaviourMark and shimGitMark are the two placeholders in shimScript.
const (
	shimBehaviourMark string = "__BEHAVIOUR__"
	shimGitMark       string = "__GIT__"
)

// TestAShallowProbeThatWillNotAnswerDegrades pins the difference between "this
// repository has full history" and "nothing told me either way".
//
// `git rev-parse --is-shallow-repository` landed in git 2.15. Against an older
// one the flag is read as a revision and the invocation fails; against a git
// that answers something else, the answer is not one of the two words. Both
// used to produce shallow=false, so Resolve computed a merge-base against
// history it had no way to know was truncated, and handed back a scoped set.
// Observed before the fix, both rows: Degraded=false — a trusted set on an
// unverified history.
//
// It uses a git shim on PATH rather than an old git, because the fact under
// test is what THIS package does with a probe that does not answer, and a shim
// reproduces that exactly. t.Setenv forbids t.Parallel, so this test owns the
// process environment for its duration.
func TestAShallowProbeThatWillNotAnswerDegrades(t *testing.T) {
	type tc struct {
		name string
		//: behaviour is the shell the shim runs for the shallow probe.
		behaviour string
		//: wantDegraded is false only for the control row.
		wantDegraded bool
	}
	tests := []tc{
		{
			name:         "a git that answers normally still resolves",
			behaviour:    ":",
			wantDegraded: false,
		},
		{
			name:         "a git too old for the flag degrades",
			behaviour:    `echo "fatal: ambiguous argument" >&2; exit 128`,
			wantDegraded: true,
		},
		{
			name:         "a git whose answer is neither word degrades",
			behaviour:    `echo maybe; exit 0`,
			wantDegraded: true,
		},
	}
	realGit, lookErr := exec.LookPath("git")
	//: Without a real git there is nothing to forward to.
	if lookErr != nil {
		t.Skipf("no git on PATH: %v", lookErr)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := initRepoOnFeature(t)
		writeRepoFile(t, root, "base.go", "package p\n\nfunc Base() int { return 1 }\n\nfunc Added() int { return 2 }\n")
		runGit(t, root, "commit", "-am", "edit")

		shimDir := t.TempDir()
		shim := filepath.Join(shimDir, "git")
		script := strings.Replace(shimScript, shimBehaviourMark, c.behaviour, 1)
		script = strings.Replace(script, shimGitMark, realGit, 1)
		//: The shim has to be executable, and nobody else's to rewrite.
		if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
			t.Fatalf("write shim: %v", err)
		}
		//: The shim shadows git; the rest of PATH stays so git keeps working.
		t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: root})
		//: A probe that did not answer must not produce a trusted set.
		if res.Degraded() != c.wantDegraded {
			t.Fatalf("Degraded() = %v (reason %q), want %v", res.Degraded(), res.Reason, c.wantDegraded)
		}
		//: A degrade is only useful if it says what happened.
		if c.wantDegraded && !strings.Contains(res.Reason, "shallow probe") {
			t.Fatalf("Reason = %q, want it to name the shallow probe", res.Reason)
		}
		//: The control row proves the shim is not the thing degrading.
		if !c.wantDegraded && !res.Set.ContainsLine(filepath.Join(root, "base.go"), 5) {
			t.Fatal("the control row lost the changed line — the shim is interfering")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
