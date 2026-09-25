// Package git_test closes ADR 0076 §Deferred against real repositories: a root
// reached through a symbolic link, a pruned origin/HEAD, a repository that
// chooses its own diff prefixes, and a copy's source.
package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// TestResolveAnswersThroughTheRootTheCallerGave pins the direction that costs
// a caller everything: git canonicalises --show-toplevel, so a repository
// reached through a symbolic link was recorded under a root the caller never
// spells, and every query against it answered false.
//
// Observed before the fix, on this fixture: Degraded=false, IsEmpty=false —
// a set that reported changes existed — with ContainsFile, ContainsLine and
// ContainsDir all false for the file that had just been edited. A scoped
// review over it examines nothing and passes.
func TestResolveAnswersThroughTheRootTheCallerGave(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	real := filepath.Join(base, "real")
	//: The repository lives at its real path; the caller will never name it.
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", real, err)
	}
	runGit(t, real, "init", "-b", "main")
	runGit(t, real, "config", "user.email", "test@example.com")
	runGit(t, real, "config", "user.name", "Test")
	writeRepoFile(t, real, "pkg/a.go", "package p\n\nfunc A() int { return 1 }\n")
	runGit(t, real, "add", ".")
	runGit(t, real, "commit", "-m", "base")
	runGit(t, real, "checkout", "-b", "feature")
	writeRepoFile(t, real, "pkg/a.go", "package p\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n")
	runGit(t, real, "commit", "-am", "edit")

	link := filepath.Join(base, "link")
	//: A platform without unprivileged symbolic links cannot exhibit this.
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}

	res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: link})
	//: The resolution itself succeeds — that is what made this silent.
	if res.Degraded() {
		t.Fatalf("Resolve through a linked root degraded: %s", res.Reason)
	}
	viaLink := filepath.Join(link, "pkg", "a.go")
	//: The three granularities, each through the caller's own spelling.
	if !res.Set.ContainsFile(viaLink) {
		t.Errorf("ContainsFile(%s) = false; the file was edited on this branch", viaLink)
	}
	if !res.Set.ContainsLine(viaLink, 5) {
		t.Errorf("ContainsLine(%s, 5) = false; line 5 is the added one", viaLink)
	}
	if !res.Set.ContainsDir(filepath.Join(link, "pkg")) {
		t.Errorf("ContainsDir(%s) = false; it encloses the edited file", filepath.Join(link, "pkg"))
	}
	//: And git's own spelling keeps answering: the rewrite adds a second
	//: accepted form, it does not replace the first.
	canonical := runGit(t, link, "rev-parse", "--show-toplevel")
	viaCanonical := filepath.Join(canonical, "pkg", "a.go")
	if !res.Set.ContainsFile(viaCanonical) || !res.Set.ContainsLine(viaCanonical, 5) {
		t.Errorf("the canonical spelling %s stopped answering", viaCanonical)
	}
}

// TestResolveAnswersUnderTheTemporaryDirectorysOwnSpelling pins the spelling a
// caller does not choose at all: the one the operating system hands it for a
// temporary directory, with no link planted by anybody.
//
// On a Windows CI runner that spelling is an 8.3 short name
// (`C:\Users\RUNNER~1\…`) while git answers `--show-toplevel` with the long
// one, `/`-separated (`C:/Users/runneradmin/…`). The root was kept in git's
// form while every recorded path went through filepath.Join, so the rewrite to
// the caller's spelling — a prefix match on that root — never matched, and a
// resolution that was neither degraded nor empty answered false for the file
// that had just been edited. pkg/v1/git's filter test found it on its first
// Windows run. On macOS the same test crosses the /var -> /private/var link;
// on Linux the two spellings coincide and it is the ordinary case.
func TestResolveAnswersUnderTheTemporaryDirectorysOwnSpelling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	writeRepoFile(t, root, "pkg/a.go", "package p\n\nfunc A() int { return 1 }\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "base")
	runGit(t, root, "checkout", "-b", "feature")
	writeRepoFile(t, root, "pkg/a.go", "package p\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n")
	runGit(t, root, "commit", "-am", "edit")

	resolved, err := filepath.EvalSymlinks(root)
	//: the temporary directory's fully resolved spelling.
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", root, err)
	}
	res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: root})
	//: the resolver answered, rather than degrading.
	if res.Degraded() {
		t.Fatalf("Resolve on the temporary directory degraded: %s", res.Reason)
	}
	//: the caller's spelling, and the fully resolved one — both must answer.
	for _, spelling := range []string{root, resolved} {
		file := filepath.Join(spelling, "pkg", "a.go")
		//: the edited file is in the set,
		if !res.Set.ContainsFile(file) {
			t.Errorf("ContainsFile(%s) = false; the file was edited on this branch", file)
		}
		//: its added line too,
		if !res.Set.ContainsLine(file, 5) {
			t.Errorf("ContainsLine(%s, 5) = false; line 5 is the added one", file)
		}
		//: and the directory that encloses it.
		if !res.Set.ContainsDir(filepath.Join(spelling, "pkg")) {
			t.Errorf("ContainsDir(%s) = false; it encloses the edited file", filepath.Join(spelling, "pkg"))
		}
	}
}

// TestResolveFallsThroughAPrunedOriginHEAD pins the state every clone taken
// before an upstream renamed its default branch is in: refs/remotes/origin/HEAD
// still points at refs/remotes/origin/master, which no longer exists.
//
// `symbolic-ref` reports a pruned symref exactly as happily as a live one, so
// the base ref was "origin/master" and the merge-base against it failed.
// Observed before the fix: Degraded=true, Reason="no merge-base with
// origin/master — everything in scope", on a repository where origin/main was
// present the whole time.
func TestResolveFallsThroughAPrunedOriginHEAD(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	up := filepath.Join(base, "up")
	//: The upstream, whose default branch is master at clone time.
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", up, err)
	}
	runGit(t, up, "init", "-b", "master")
	runGit(t, up, "config", "user.email", "test@example.com")
	runGit(t, up, "config", "user.name", "Test")
	writeRepoFile(t, up, "a.go", "package p\n\nfunc A() int { return 1 }\n")
	runGit(t, up, "add", ".")
	runGit(t, up, "commit", "-m", "base")

	work := filepath.Join(base, "work")
	runGit(t, base, "clone", "--quiet", up, work)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "Test")
	//: The upstream renames its default branch; the clone prunes the old
	//: remote-tracking ref and leaves origin/HEAD pointing at it.
	runGit(t, up, "branch", "-m", "master", "main")
	runGit(t, work, "fetch", "--quiet", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*")
	runGit(t, work, "checkout", "-b", "feature")
	writeRepoFile(t, work, "a.go", "package p\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n")
	runGit(t, work, "commit", "-am", "edit")

	//: The fixture is only the fixture if the symref really is pruned.
	if got := runGit(t, work, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/master" {
		t.Fatalf("fixture: origin/HEAD = %q, want the pruned refs/remotes/origin/master", got)
	}

	res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: work})
	//: origin/main exists; there is nothing to degrade about.
	if res.Degraded() {
		t.Fatalf("Resolve degraded on a pruned origin/HEAD: %s", res.Reason)
	}
	if res.BaseRef != "origin/main" {
		t.Errorf("BaseRef = %q, want origin/main", res.BaseRef)
	}
	edited := filepath.Join(runGit(t, work, "rev-parse", "--show-toplevel"), "a.go")
	//: And the set it produced is the real one.
	if !res.Set.ContainsLine(edited, 5) {
		t.Errorf("ContainsLine(%s, 5) = false after falling through to origin/main", edited)
	}
}

// TestResolveKeepsLineRangesUnderRepositoryChosenDiffPrefixes pins the third
// group of hostile `.git/config` keys, the one that executes nothing.
//
// `diff.srcPrefix`/`diff.dstPrefix` and `diff.mnemonicPrefix` rename the a//b/
// prefixes of a unified-diff header. The "b/" strip then leaves the prefix in
// place, the line ranges are filed under <root>/DST/a.go or <root>/w/a.go, and
// the file a caller asks about answers false for every line it changed.
// Observed before the fix, both rows: ContainsFile true, ContainsLine(5) FALSE.
// ContainsFile survives because the NUL-separated name-status pass carries no
// prefixes — which is the two-pass design doing exactly what it is for, and
// also why nothing else was visibly wrong.
func TestResolveKeepsLineRangesUnderRepositoryChosenDiffPrefixes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		keys [][2]string
		//: commit decides which diff source carries the change, because
		//: diff.mnemonicPrefix only applies to index/worktree comparisons.
		commit bool
	}
	tests := []tc{
		{
			name:   "no key set",
			keys:   nil,
			commit: true,
		},
		{
			name:   "diff.srcPrefix and diff.dstPrefix",
			keys:   [][2]string{{"diff.srcPrefix", "SRC/"}, {"diff.dstPrefix", "DST/"}},
			commit: true,
		},
		{
			name:   "diff.noprefix",
			keys:   [][2]string{{"diff.noprefix", "true"}},
			commit: true,
		},
		{
			name:   "diff.mnemonicPrefix on a working-tree change",
			keys:   [][2]string{{"diff.mnemonicPrefix", "true"}},
			commit: false,
		},
		{
			name:   "diff.noprefix overriding the explicit prefixes",
			keys:   [][2]string{{"diff.noprefix", "true"}, {"diff.srcPrefix", "SRC/"}, {"diff.dstPrefix", "DST/"}},
			commit: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := initRepoOnFeature(t)
		//: The keys travel with a clone, so they are set in the repository
		//: itself rather than passed on a command line.
		for _, kv := range c.keys {
			runGit(t, root, "config", kv[0], kv[1])
		}
		writeRepoFile(t, root, "base.go", "package p\n\nfunc Base() int { return 1 }\n\nfunc Added() int { return 2 }\n")
		//: A committed change exercises the merge-base diff; an uncommitted
		//: one exercises the index and working-tree diffs.
		if c.commit {
			runGit(t, root, "commit", "-am", "edit")
		}
		res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: root})
		//: Nothing here prevents a trustworthy answer.
		if res.Degraded() {
			t.Fatalf("Resolve degraded: %s", res.Reason)
		}
		edited := filepath.Join(root, "base.go")
		//: Membership was never at risk — the -z pass has no prefixes.
		if !res.Set.ContainsFile(edited) {
			t.Fatalf("ContainsFile(%s) = false", edited)
		}
		//: The line range is what the prefixes stole.
		if !res.Set.ContainsLine(edited, 5) {
			t.Fatalf("ContainsLine(%s, 5) = false — the added line lost its range", edited)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestACopysSourceIsAlreadyInTheSetForItsOwnReason is the measurement ADR 0076
// §Deferred asked for and did not take.
//
// The entry reads: `git diff -M -C` reports `C### old new`, both sides go
// through markPath, so the unchanged `old` enters the set. The first half is
// true and the conclusion does not follow. Without --find-copies-harder, which
// this package does not pass and which no configuration key can turn on, git
// only offers a copy whose SOURCE was itself modified in the same changeset —
// so the source always carries its own record, and removing the copy's marking
// would remove nothing. This test pins the record shapes the claim rests on:
// if a future change adds --find-copies-harder, the name-status assertion here
// is what stops being true.
func TestACopysSourceIsAlreadyInTheSetForItsOwnReason(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: build leaves the branch in the state under test.
		build func(t *testing.T, root string)
		//: wantCopy is the copy record whose source the entry is about.
		wantCopy string
		//: wantSourceOwnRecord is the record that puts the source in the set
		//: WITHOUT the copy — the whole of the measurement.
		wantSourceOwnRecord string
		//: present is what the resolved set must contain.
		present []string
	}
	tests := []tc{
		{
			name: "a copy whose source was modified",
			build: func(t *testing.T, root string) {
				t.Helper()
				writeRepoFile(t, root, "b.go", "package p\n\nfunc Base() int { return 1 }\n")
				writeRepoFile(t, root, "base.go", "package p\n\nfunc Base() int { return 1 }\n\nfunc More() int { return 2 }\n")
				runGit(t, root, "add", ".")
				runGit(t, root, "commit", "-m", "copy and modify")
			},
			wantCopy:            "C100|base.go|b.go|",
			wantSourceOwnRecord: "M|base.go|",
			present:             []string{"base.go", "b.go"},
		},
		{
			name: "a copy whose source was deleted",
			build: func(t *testing.T, root string) {
				t.Helper()
				writeRepoFile(t, root, "b.go", "package p\n\nfunc Base() int { return 1 }\n")
				writeRepoFile(t, root, "c.go", "package p\n\nfunc Base() int { return 1 }\n")
				//: The source leaves, so git reports one rename and one copy.
				if err := os.Remove(filepath.Join(root, "base.go")); err != nil {
					t.Fatalf("remove base.go: %v", err)
				}
				runGit(t, root, "add", "-A")
				runGit(t, root, "commit", "-m", "two copies, source gone")
			},
			wantCopy:            "C100|base.go|b.go|",
			wantSourceOwnRecord: "R100|base.go|c.go|",
			present:             []string{"base.go", "b.go", "c.go"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := initRepoOnFeature(t)
		c.build(t, root)
		//: The claim is about git's OUTPUT, so the output is the assertion.
		//: A leading separator so a record match cannot start mid-record;
		//: git orders records by the new path and that order is not the claim.
		payload := "|" + strings.ReplaceAll(runGit(t, root, "diff", "--name-status", "-z", "-M", "-C", "main", "HEAD"), "\x00", "|")
		//: The copy really is reported, or the row proves nothing.
		if !strings.Contains(payload, "|"+c.wantCopy) {
			t.Fatalf("name-status = %q, want a %q record", payload, c.wantCopy)
		}
		//: And the source carries its own record, which is why dropping the
		//: copy's marking would remove nothing from the set.
		if !strings.Contains(payload, "|"+c.wantSourceOwnRecord) {
			t.Fatalf("name-status = %q, want the source's own %q record", payload, c.wantSourceOwnRecord)
		}
		res := gitpkg.Resolve(t.Context(), gitpkg.Config{Root: root})
		//: Nothing degrades here.
		if res.Degraded() {
			t.Fatalf("Resolve degraded: %s", res.Reason)
		}
		//: Every named path is in the set, the source included.
		for _, rel := range c.present {
			if !res.Set.ContainsFile(filepath.Join(root, rel)) {
				t.Errorf("ContainsFile(%s) = false", rel)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
