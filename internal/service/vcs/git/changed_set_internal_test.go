// Package git white-box tests the changed-set builders.
package git

import (
	"path/filepath"
	"testing"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
)

// TestChangedSetValue_addLineRange verifies recorded ranges drive ContainsLine
// with correct inclusive boundaries.
func TestChangedSetValue_addLineRange(t *testing.T) {
	t.Parallel()

	root := filepath.FromSlash("/repo")
	file := filepath.Join(root, "foo.go")
	set := NewChangedSetValue(root)
	set.addLineRange(file, corevcs.LineRangeValue{Start: 10, End: 12})

	tests := []struct {
		name string
		line int
		want bool
	}{
		{name: "below range", line: 9, want: false},
		{name: "range start", line: 10, want: true},
		{name: "range mid", line: 11, want: true},
		{name: "range end", line: 12, want: true},
		{name: "above range", line: 13, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Each line must match its expected membership.
			if got := set.ContainsLine(file, tt.line); got != tt.want {
				t.Errorf("ContainsLine(%d) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// TestChangedSetValue_addWholeFile verifies a whole-file entry matches any line.
func TestChangedSetValue_addWholeFile(t *testing.T) {
	t.Parallel()
	set := NewChangedSetValue(filepath.FromSlash("/repo"))
	set.addWholeFile(filepath.FromSlash("/repo/whole.go"))
	//: A whole-file change matches an arbitrarily high line.
	if !set.ContainsLine(filepath.FromSlash("/repo/whole.go"), 9999) {
		t.Error("addWholeFile should match any line")
	}
}

// TestChangedSetValue_markTouched verifies a touched-only path marks its package
// dir without recording any changed line.
func TestChangedSetValue_markTouched(t *testing.T) {
	t.Parallel()
	set := NewChangedSetValue(filepath.FromSlash("/repo"))
	set.markTouched(filepath.FromSlash("/repo/sub/x.go"))
	//: The package dir is touched even though no line range was recorded.
	if !set.ContainsDir(filepath.FromSlash("/repo/sub")) {
		t.Error("markTouched should touch the package dir")
	}
}

// Test_spelledAs covers the second spelling recorded for a caller that reached
// the repository through a symbolic link. It needs no link: the alias is a
// recorded string, so the mapping is exercised directly on every platform,
// including the ones where a link needs a privilege.
func Test_spelledAs(t *testing.T) {
	t.Parallel()
	repo := filepath.FromSlash("/canonical/repo")
	alias := filepath.FromSlash("/spelled/link")
	type tc struct {
		name string
		//: root overrides the canonical repository root; empty uses repo.
		root   string
		alias  string
		path   string
		want   string
		wantOK bool
	}
	tests := []tc{
		{
			name:   "no alias mirrors nothing",
			alias:  "",
			path:   filepath.Join(repo, "a.go"),
			want:   "",
			wantOK: false,
		},
		{
			name:   "a path under the root is mirrored under the alias",
			alias:  alias,
			path:   filepath.Join(repo, "pkg", "a.go"),
			want:   filepath.Join(alias, "pkg", "a.go"),
			wantOK: true,
		},
		{
			name:   "a sibling whose name merely starts with the root is not mirrored",
			alias:  alias,
			path:   repo + "-other" + string(filepath.Separator) + "a.go",
			want:   "",
			wantOK: false,
		},
		{
			name:   "an unrelated path is not mirrored",
			alias:  alias,
			path:   filepath.FromSlash("/elsewhere/a.go"),
			want:   "",
			wantOK: false,
		},
		{
			//: A repository rooted at the filesystem root: repoRoot already
			//: ends in a separator, so appending one makes "//" — a prefix no
			//: cleaned path carries, and the whole tree mirrors nothing.
			name:   "a root that already ends in a separator still mirrors",
			root:   string(filepath.Separator),
			alias:  alias,
			path:   filepath.FromSlash("/a.go"),
			want:   filepath.Join(alias, "a.go"),
			wantOK: true,
		},
		{
			name:   "a nested path under a separator-terminated root mirrors too",
			root:   string(filepath.Separator),
			alias:  alias,
			path:   filepath.FromSlash("/sub/a.go"),
			want:   filepath.Join(alias, "sub", "a.go"),
			wantOK: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := repo
		//: Rows that exercise a separator-terminated root say so.
		if c.root != "" {
			root = c.root
		}
		set := newChangedSetValue(root, c.alias)
		got, ok := set.spelledAs(c.path)
		//: A wrong mirror would record a path nothing will ever query, and a
		//: missing one leaves the caller's own spelling answering false.
		if got != c.want || ok != c.wantOK {
			t.Fatalf("spelledAs(%q) with alias %q = (%q, %v), want (%q, %v)", c.path, c.alias, got, ok, c.want, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_bothSpellingsAnswer pins the consequence: a set built with an alias
// answers for the caller's root AND for git's, at all three granularities.
func Test_bothSpellingsAnswer(t *testing.T) {
	t.Parallel()
	repo := filepath.FromSlash("/canonical/repo")
	alias := filepath.FromSlash("/spelled/link")
	set := newChangedSetValue(repo, alias)
	set.addLineRange(filepath.Join(repo, "pkg", "a.go"), corevcs.LineRangeValue{Start: 4, End: 6})
	set.markTouched(filepath.Join(repo, "pkg", "gone.go"))

	for _, root := range []string{repo, alias} {
		//: Every question a caller can ask, under each root.
		if !set.ContainsLine(filepath.Join(root, "pkg", "a.go"), 5) {
			t.Errorf("ContainsLine under %s = false", root)
		}
		if !set.ContainsFile(filepath.Join(root, "pkg", "gone.go")) {
			t.Errorf("ContainsFile under %s = false", root)
		}
		if !set.ContainsDir(filepath.Join(root, "pkg")) {
			t.Errorf("ContainsDir under %s = false", root)
		}
		//: And a path neither root touched stays out.
		if set.ContainsFile(filepath.Join(root, "pkg", "untouched.go")) {
			t.Errorf("ContainsFile(untouched) under %s = true", root)
		}
	}
}
