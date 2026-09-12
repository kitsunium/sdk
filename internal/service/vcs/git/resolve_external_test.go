// Package git_test exercises changed-set resolution against real temp repos.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
)

// runGit runs a git subcommand in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(t.Context(), "git", full...)
	out, err := cmd.CombinedOutput()
	//: A git failure in a fixture is a test-setup bug, not a soft condition.
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	//: Return trimmed stdout for callers that need it (e.g. toplevel).
	return strings.TrimSpace(string(out))
}

// writeRepoFile writes content to dir/rel, creating parent directories.
func writeRepoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, rel)
	//: Parent dirs must exist before the write.
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	//: A failed fixture write invalidates the test.
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// initRepo creates a git repo on branch main with one committed file and
// returns the git top-level path (symlink-canonicalised so it matches the
// absolute paths Resolve records).
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeRepoFile(t, dir, "base.go", "package p\n\nfunc Base() int { return 1 }\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")
	return runGit(t, dir, "rev-parse", "--show-toplevel")
}

// initRepoOnFeature creates the base repo and checks out a fresh feature branch.
func initRepoOnFeature(t *testing.T) string {
	t.Helper()
	root := initRepo(t)
	runGit(t, root, "checkout", "-b", "feature")
	return root
}

// TestResolve drives changed-set resolution across the scenario matrix. Each
// row owns its full repository setup and assertions.
func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		check func(t *testing.T, root string, res corevcs.ResolutionValue)
	}{
		{
			name:  "no git repository falls back to full scan",
			setup: func(t *testing.T) string { t.Helper(); return t.TempDir() },
			check: func(t *testing.T, _ string, res corevcs.ResolutionValue) {
				t.Helper()
				//: No .git → loud full-scan fallback with a reason.
				if !res.FullFallback || res.Reason == "" {
					t.Fatalf("expected loud FullFallback, got %+v", res)
				}
			},
		},
		{
			name:  "clean branch resolves to an empty set",
			setup: initRepoOnFeature,
			check: func(t *testing.T, _ string, res corevcs.ResolutionValue) {
				t.Helper()
				//: A resolvable clean branch is NOT a fallback.
				if res.FullFallback {
					t.Fatalf("unexpected fallback: %s", res.Reason)
				}
				//: With no changes the set is empty.
				if res.Set == nil || !res.Set.IsEmpty() {
					t.Fatalf("expected empty set, got %+v", res.Set)
				}
			},
		},
		{
			name: "modified hunk surfaces only the changed file and line",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "base.go", "package p\n\nfunc Base() int { return 1 }\n\nfunc Added() int { return 2 }\n")
				runGit(t, root, "commit", "-am", "edit")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				set := res.Set
				//: Resolution must succeed with a populated set.
				if res.FullFallback || set == nil {
					t.Fatalf("unexpected fallback/nil: %+v", res)
				}
				base := filepath.Join(root, "base.go")
				//: The edited file and its new line (5) are in the diff.
				if !set.ContainsFile(base) || !set.ContainsLine(base, 5) {
					t.Errorf("edited base.go line 5 not in diff: %+v", set)
				}
				//: An untouched file is absent.
				if set.ContainsFile(filepath.Join(root, "other.go")) {
					t.Error("untouched other.go reported in diff")
				}
				//: The package directory is touched.
				if !set.ContainsDir(root) {
					t.Error("package dir not touched")
				}
			},
		},
		{
			name: "new untracked file is wholly in the diff",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "fresh.go", "package p\n\nfunc Fresh() {}\n")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				fresh := filepath.Join(root, "fresh.go")
				//: A brand-new file is fully in the diff — any line matches.
				if !res.Set.ContainsLine(fresh, 3) || !res.Set.ContainsFile(fresh) {
					t.Errorf("new file not wholly in diff: %+v", res.Set)
				}
			},
		},
		{
			name: "staged uncommitted change is included",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "staged.go", "package p\n\nfunc Staged() {}\n")
				runGit(t, root, "add", "staged.go")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				//: A staged-but-uncommitted file must be visible (agent/IDE flow).
				if !res.Set.ContainsFile(filepath.Join(root, "staged.go")) {
					t.Error("staged file not in changed-set")
				}
			},
		},
		{
			name: "unstaged working-tree edit is included",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "base.go", "package p\n\nfunc Base() int { return 42 }\n")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				//: The uncommitted working-tree edit must be in the set.
				if !res.Set.ContainsLine(filepath.Join(root, "base.go"), 3) {
					t.Error("unstaged edit not in changed-set")
				}
			},
		},
		{
			name: "pure rename touches the file without changed lines",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				runGit(t, root, "mv", "base.go", "renamed.go")
				runGit(t, root, "commit", "-am", "rename")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				renamed := filepath.Join(root, "renamed.go")
				//: The moved file is file/package-touched...
				if !res.Set.ContainsFile(renamed) {
					t.Error("renamed file should be file-touched")
				}
				//: ...but a pure rename adds no changed lines.
				if res.Set.ContainsLine(renamed, 3) {
					t.Error("pure rename must not record changed lines")
				}
			},
		},
		{
			name: "deletion keeps the package dir touched",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepo(t)
				writeRepoFile(t, root, "sub/keep.go", "package sub\n\nfunc Keep() {}\n")
				writeRepoFile(t, root, "sub/gone.go", "package sub\n\nfunc Gone() {}\n")
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-m", "two files")
				runGit(t, root, "checkout", "-b", "feature")
				runGit(t, root, "rm", "sub/gone.go")
				runGit(t, root, "commit", "-am", "delete gone")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				//: The deleted file's package dir must stay touched.
				if !res.Set.ContainsDir(filepath.Join(root, "sub")) {
					t.Error("deleted file's package dir should be touched")
				}
			},
		},
		{
			name: "generated file is excluded from the diff",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "zz_gen.go", "// Code generated by tool. DO NOT EDIT.\npackage p\n\nfunc Gen() {}\n")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				//: A generated file must not be file-touched.
				if res.Set.ContainsFile(filepath.Join(root, "zz_gen.go")) {
					t.Error("generated file leaked into changed-set")
				}
			},
		},
		{
			name: "modified accented filename keeps its line range",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepo(t)
				writeRepoFile(t, root, "café.go", "package p\n\nfunc Caf() int { return 1 }\n")
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-m", "accent base")
				runGit(t, root, "checkout", "-b", "feature")
				writeRepoFile(t, root, "café.go", "package p\n\nfunc Caf() int { return 1 }\n\nfunc CafAdded() int { return 2 }\n")
				runGit(t, root, "commit", "-am", "accent edit")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				accented := filepath.Join(root, "café.go")
				//: The non-ASCII name must not be c-quoted out of the set.
				if !res.Set.ContainsFile(accented) {
					t.Error("accented file dropped from changed-set")
				}
				//: The added line (5) carries its exact range.
				if !res.Set.ContainsLine(accented, 5) {
					t.Error("accented file lost its changed line range")
				}
				//: An untouched line stays out of the diff.
				if res.Set.ContainsLine(accented, 3) {
					t.Error("unchanged line 3 reported in diff")
				}
			},
		},
		{
			name: "modified filename with a space keeps its line range",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepo(t)
				writeRepoFile(t, root, "with space.go", "package p\n\nfunc Sp() int { return 1 }\n")
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-m", "space base")
				runGit(t, root, "checkout", "-b", "feature")
				writeRepoFile(t, root, "with space.go", "package p\n\nfunc Sp() int { return 1 }\n\nfunc SpAdded() int { return 2 }\n")
				runGit(t, root, "commit", "-am", "space edit")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				spaced := filepath.Join(root, "with space.go")
				//: The spaced name survives both diff passes with its range.
				if !res.Set.ContainsFile(spaced) || !res.Set.ContainsLine(spaced, 5) {
					t.Error("file with space missing from changed-set or lost its range")
				}
			},
		},
		{
			name: "rename of an accented file is file-touched without lines",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepo(t)
				writeRepoFile(t, root, "café.go", "package p\n\nfunc Caf() int { return 1 }\n")
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-m", "accent base")
				runGit(t, root, "checkout", "-b", "feature")
				runGit(t, root, "mv", "café.go", "thé.go")
				runGit(t, root, "commit", "-am", "accent rename")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				renamed := filepath.Join(root, "thé.go")
				//: The accented rename target is file-touched...
				if !res.Set.ContainsFile(renamed) {
					t.Error("accented rename target dropped from changed-set")
				}
				//: ...but a pure rename records no changed lines.
				if res.Set.ContainsLine(renamed, 1) {
					t.Error("pure rename must not record changed lines")
				}
				//: The old side keeps the package dir touched.
				if !res.Set.ContainsDir(root) {
					t.Error("rename did not keep the package dir touched")
				}
			},
		},
		{
			name: "untracked accented file is wholly in the diff",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "frais café.go", "package p\n\nfunc Frais() {}\n")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				fresh := filepath.Join(root, "frais café.go")
				//: ls-files -z must carry the exotic name verbatim — any line hits.
				if !res.Set.ContainsFile(fresh) || !res.Set.ContainsLine(fresh, 3) {
					t.Errorf("untracked accented file not wholly in diff: %+v", res.Set)
				}
			},
		},
		{
			name: "non-Go change does not touch the package",
			setup: func(t *testing.T) string {
				t.Helper()
				root := initRepoOnFeature(t)
				writeRepoFile(t, root, "README.md", "# changed\n")
				return root
			},
			check: func(t *testing.T, root string, res corevcs.ResolutionValue) {
				t.Helper()
				//: A README-only change must not mark the Go package touched.
				if res.Set.ContainsDir(root) {
					t.Error("non-Go change should not touch the package dir")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := tt.setup(t)
			res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: root, Include: goNonGenerated})
			tt.check(t, root, res)
		})
	}
}

// goNonGenerated is the filter the source implementation hard-coded: a ".go"
// suffix plus a generated-file probe.
//
// It lives HERE, in the suite, rather than in the package, and that placement is
// the point. Every assertion below — the generated file excluded, the non-Go
// change that must not touch a directory — was written against the hard-coded
// version. They pass unchanged against the injected one, which is what proves
// the extraction moved the policy without weakening it. Drop this filter from
// the Config and two of them fail.
func goNonGenerated(absPath string) bool {
	//: a changed README must not mark a directory touched for a Go tool.
	if !strings.HasSuffix(absPath, ".go") {
		//: not a Go file — outside this caller's policy.
		return false
	}
	content, err := os.ReadFile(absPath)
	//: a file the filter cannot read is left in scope rather than dropped
	//: silently: under-reporting a diff is the one answer that must not happen.
	if err != nil {
		//: keep it.
		return true
	}
	//: the convention is a line matching "^// Code generated .* DO NOT EDIT\.$"
	//: appearing before the package clause (see go/build).
	for line := range strings.SplitSeq(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		//: the package clause ends the header region.
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		//: a generated file is excluded so tool churn never floods a diff.
		if strings.HasPrefix(trimmed, "// Code generated ") && strings.HasSuffix(trimmed, " DO NOT EDIT.") {
			//: generated — outside this caller's policy.
			return false
		}
	}

	//: a hand-written Go file is in scope.
	return true
}
