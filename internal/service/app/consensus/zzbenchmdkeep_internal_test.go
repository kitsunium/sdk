package consensus

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// benchHandRunHeading opens the part of BENCH.md no generator writes: the
// hand-run sections of #913, #922, #923 and #1109, each refreshed by the
// command it names.
const benchHandRunHeading = "## Hand-run micro-benchmarks"

// keepHandRun is rendered followed by the hand-run part of before, the
// file it replaces, from benchHandRunHeading on: the generator rewrote the
// whole file and erased them (#1361's known follow-up). It sits in a file
// of its own, in consensus_test and in the bench lane's target alike.
func keepHandRun(rendered string, before []byte) string {
	i := strings.Index(string(before), "\n"+benchHandRunHeading)
	if i < 0 {
		return rendered
	}
	return strings.TrimRight(rendered, "\n") + "\n" + string(before[i:])
}

// benchBefore is the BENCH.md at path that the generator replaces, read
// with read: nil when there is none yet, nothing hand-run to keep, and an
// error when it exists but cannot be read. Carried on as empty, it would
// have the write erase every hand-run section.
func benchBefore(read func(string) ([]byte, error), path string) ([]byte, error) {
	b, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// benchSplice is doc with section in place of the section that opens with
// the line heading, up to the next second-level heading: a run of the
// generator that measures some sections alone (CONSENSUS_BENCH_ONLY, #1364)
// rewrites them and keeps every other byte. A doc without that section gets
// it before the first of anchors it holds, at its end otherwise.
func benchSplice(doc, heading, section string, anchors ...string) string {
	if i := benchHeadingAt(doc, heading); i >= 0 {
		end := len(doc)
		if j := strings.Index(doc[i+len(heading):], "\n## "); j >= 0 {
			end = i + len(heading) + j
		}
		return doc[:i] + section + doc[end:]
	}
	for _, anchor := range anchors {
		if i := benchHeadingAt(doc, anchor); i >= 0 {
			return doc[:i] + section + "\n" + doc[i:]
		}
	}
	return strings.TrimRight(doc, "\n") + "\n\n" + section
}

// benchHeadingAt is the offset of the first line of doc that opens with
// heading, -1 when none does.
func benchHeadingAt(doc, heading string) int {
	if strings.HasPrefix(doc, heading) {
		return 0
	}
	if i := strings.Index(doc, "\n"+heading); i >= 0 {
		return i + 1
	}
	return -1
}

// benchGitLocators are the variables that decide which repository, index and
// object store a git command reads, and how far up it looks for one. Any of
// them, inherited from a hook or a wrapper, makes `git -C dir` read another
// tree than dir (#1487).
var benchGitLocators = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
	"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// benchGitEnv is the environment of every git command the bench and its
// stamp run: the process's own without the benchGitLocators, then extra.
func benchGitEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		//: a locator is dropped so that -C names the tree git reads.
		if slices.Contains(benchGitLocators, name) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// benchGit runs git in dir under benchGitEnv(extra...) and returns its
// trimmed output.
func benchGit(dir string, extra []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = benchGitEnv(extra...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// benchCommitStamp is the commit a bench run is dated by, in the git tree
// at dir: the HEAD's short SHA when the tree is that commit, "uncommitted
// tree on <sha>" when anything differs from it — a modified, staged or
// untracked file — because a bench measured on such a tree did not run on
// the SHA (#1487), and "unknown" without a git tree. extra is added to the
// git environment, which holds no repository locator. It sits in this file
// for the same reason as keepHandRun: the generator's lane and
// consensus_test both compile it.
func benchCommitStamp(dir string, extra ...string) string {
	sha, err := benchGit(dir, extra, "rev-parse", "--short", "HEAD")
	//: no HEAD to name: not a git tree, or one without a commit.
	if err != nil {
		return "unknown"
	}
	status, err := benchGit(dir, extra, "status", "--porcelain", "--untracked-files=all")
	//: a tree whose state cannot be read is not claimed to be the commit.
	if err != nil {
		return sha + " (tree state unknown)"
	}
	//: any line is a path that differs from HEAD.
	if status != "" {
		return "uncommitted tree on " + sha
	}
	return sha
}

// A run that measures some sections alone rewrites each in place and keeps
// every other byte of BENCH.md, the rows of the devices it did not measure
// and the hand-run part included; a section the file does not hold yet goes
// before the first anchor it holds (#1364).
func TestTheBenchGeneratorRewritesASectionInPlace(t *testing.T) {
	const (
		head    = "<!-- generated -->\n# Benchmarks\n\n## Host\n\n| CPU | x |\n"
		turn    = "## Turn\n\nold turn rows\n"
		targets = "## Targets\n\n| met |\n"
		hand    = "## Hand-run micro-benchmarks (#913)\n\nmeasured by hand\n"
		fresh   = "## Turn\n\nnew turn rows\n"
	)
	type tc struct {
		name    string
		doc     string
		anchors []string
		want    string
	}
	cases := []tc{
		{
			name: "the section in place, between two others",
			doc:  head + "\n" + turn + "\n" + targets + "\n" + hand,
			want: head + "\n" + fresh + "\n" + targets + "\n" + hand,
		},
		{
			name: "the section last of the file",
			doc:  head + "\n" + turn,
			want: head + "\n" + fresh,
		},
		{
			name:    "no such section: before the first anchor the file holds",
			doc:     head + "\n" + targets + "\n" + hand,
			anchors: []string{"## Reproduce", "## Targets"},
			want:    head + "\n" + fresh + "\n" + targets + "\n" + hand,
		},
		{
			name: "no such section, no anchor: at the end",
			doc:  head,
			want: head + "\n" + fresh,
		},
		{
			name: "a heading in a line's middle is no section",
			doc:  head + "see ## Turn below\n",
			want: head + "see ## Turn below\n\n" + fresh,
		},
	}
	runCase := func(t *testing.T, c tc) {
		if got := benchSplice(c.doc, "## Turn", fresh, c.anchors...); got != c.want {
			t.Fatalf("the rewritten file is\n%s\nwant\n%s", got, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// make bench rewrote all of BENCH.md and erased the hand-run sections of
// #913, #922, #923 and #1109 below the generator's output, which only
// their own commands refresh (#1361's known follow-up): the generator
// keeps everything from benchHandRunHeading on.
func TestTheBenchGeneratorKeepsTheHandRunSections(t *testing.T) {
	const hand = "## Hand-run micro-benchmarks (#913)\n\nmeasured by hand\n"
	type tc struct {
		name string
		// rendered is the generator's output, before the file it replaces.
		rendered string
		before   []byte
		// ok judges the regenerated file.
		ok func(got string) bool
	}
	cases := []tc{
		{
			name:     "a hand-run part before",
			rendered: "<!-- generated -->\n# Benchmarks\n\n| new | row |\n",
			before:   []byte("<!-- generated -->\n# Benchmarks\n\n| old | row |\n\n" + hand),
			ok: func(got string) bool {
				return strings.HasSuffix(got, "\n\n"+hand) && strings.Contains(got, "| new | row |") && !strings.Contains(got, "| old | row |")
			},
		},
		{
			name:     "no file before",
			rendered: "generated\n",
			before:   nil,
			ok:       func(got string) bool { return got == "generated\n" },
		},
		{
			name:     "no hand-run part before",
			rendered: "generated\n",
			before:   []byte("no hand-run part\n"),
			ok:       func(got string) bool { return got == "generated\n" },
		},
	}
	runCase := func(t *testing.T, c tc) {
		if got := keepHandRun(c.rendered, c.before); !c.ok(got) {
			t.Fatalf("the regenerated file is\n%s", got)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The generator read the BENCH.md it replaces ignoring the error: a file
// it could not read was taken as empty and the write erased its hand-run
// sections. A missing file is the only one taken as empty.
func TestTheBenchGeneratorWritesNothingOverAReportItCannotRead(t *testing.T) {
	type tc struct {
		name string
		// read answers the generator's read of the report.
		read    func(string) ([]byte, error)
		want    string
		wantErr bool
	}
	cases := []tc{
		{name: "read", read: func(string) ([]byte, error) { return []byte("report"), nil }, want: "report"},
		{name: "none yet", read: func(p string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}},
		{name: "unreadable", read: func(p string) ([]byte, error) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
		}, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		got, err := benchBefore(c.read, "BENCH.md")
		if (err != nil) != c.wantErr || string(got) != c.want {
			t.Fatalf("benchBefore = %q, %v; want %q, an error %t", got, err, c.want, c.wantErr)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The generator dated a run by the HEAD of the tree, so a bench measured
// on uncommitted work read "at <sha>" for a commit the bench did not run
// on (#1487): a clean tree is the commit, any change, a staged one or a
// file git does not track included, is "uncommitted tree on <sha>".
func TestTheBenchStampNamesTheTreeTheBenchRanOn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: the stamp reads a git tree")
	}
	type tc struct {
		name string
		// edit changes the tree after its one commit.
		edit func(t *testing.T, dir string)
		// dirty is whether the stamp must say the tree is not the commit.
		dirty bool
	}
	cases := []tc{
		{name: "a clean tree", edit: func(*testing.T, string) {}},
		{name: "a tracked file modified", dirty: true, edit: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "tracked.go"), []byte("package x // edited\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a file git does not track", dirty: true, edit: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "bench_test.go"), []byte("package x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a change staged and not committed", dirty: true, edit: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, dir, "add", "staged.go")
		}},
	}
	runCase := func(t *testing.T, c tc) {
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q")
		if err := os.WriteFile(filepath.Join(dir, "tracked.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", "tracked.go")
		gitIn(t, dir, "-c", "user.name=bench", "-c", "user.email=bench@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "one")
		sha := strings.TrimSpace(gitIn(t, dir, "rev-parse", "--short", "HEAD"))
		c.edit(t, dir)
		want := sha
		if c.dirty {
			want = "uncommitted tree on " + sha
		}
		if got := benchCommitStamp(dir); got != want {
			t.Fatalf("benchCommitStamp = %q, want %q", got, want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
	t.Run("not a git tree", func(t *testing.T) {
		dir := t.TempDir()
		// No parent of dir may be a repository the stamp could find.
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		if got := benchCommitStamp(dir); got != "unknown" {
			t.Fatalf("benchCommitStamp outside a repository = %q, want unknown", got)
		}
	})
}

// A git command of the stamp read the repository its environment named,
// not the one -C gave it: a hook's GIT_DIR, GIT_INDEX_FILE or GIT_WORK_TREE
// made a clean tree read as another commit or as a dirty one (#1487). Every
// locator is cleared from the commands' one environment.
func TestTheBenchStampIgnoresTheRepositoryTheEnvironmentNames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed: the stamp reads a git tree")
	}
	type tc struct {
		name string
		// env gives the variable and its value, other being a second tree.
		env func(dir, other string) (key, value string)
	}
	cases := []tc{
		{name: "GIT_DIR", env: func(_, other string) (string, string) { return "GIT_DIR", filepath.Join(other, ".git") }},
		{name: "GIT_WORK_TREE", env: func(_, other string) (string, string) { return "GIT_WORK_TREE", other }},
		{name: "GIT_INDEX_FILE", env: func(_, other string) (string, string) {
			return "GIT_INDEX_FILE", filepath.Join(other, ".git", "index")
		}},
		{name: "GIT_COMMON_DIR", env: func(_, other string) (string, string) { return "GIT_COMMON_DIR", filepath.Join(other, ".git") }},
	}
	commit := func(t *testing.T, dir, file string) string {
		gitIn(t, dir, "init", "-q")
		if err := os.WriteFile(filepath.Join(dir, file), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", file)
		gitIn(t, dir, "-c", "user.name=bench", "-c", "user.email=bench@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "-q", "-m", file)
		return strings.TrimSpace(gitIn(t, dir, "rev-parse", "--short", "HEAD"))
	}
	runCase := func(t *testing.T, c tc) {
		dir, other := t.TempDir(), t.TempDir()
		want := commit(t, dir, "tracked.go")
		commit(t, other, "elsewhere.go")
		key, value := c.env(dir, other)
		t.Setenv(key, value)
		if got := benchCommitStamp(dir); got != want {
			t.Fatalf("with %s=%s, benchCommitStamp = %q, want the clean tree's %q", key, value, got, want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// gitIn runs git in dir, in the bench's own git environment, and fails the
// test on an error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = benchGitEnv()
	out, err := cmd.CombinedOutput()
	//: a failed git leaves the test nothing to judge.
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
