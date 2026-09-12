// Package git white-box tests the unified-diff parser.
package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// Test_parseStartCount verifies +side token parsing including the bare-count
// default and the error path.
func Test_parseStartCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		token     string
		wantStart int
		wantCount int
		wantOK    bool
	}{
		{name: "pair", token: "12,3", wantStart: 12, wantCount: 3, wantOK: true},
		{name: "bare defaults count to 1", token: "7", wantStart: 7, wantCount: 1, wantOK: true},
		{name: "zero count", token: "5,0", wantStart: 5, wantCount: 0, wantOK: true},
		{name: "non-numeric start", token: "x,1", wantOK: false},
		{name: "non-numeric count", token: "5,y", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start, count, ok := parseStartCount(tt.token)
			//: The ok flag is the primary contract.
			if ok != tt.wantOK {
				t.Fatalf("parseStartCount(%q) ok = %v, want %v", tt.token, ok, tt.wantOK)
			}
			//: Start/count only meaningful when ok.
			if ok && (start != tt.wantStart || count != tt.wantCount) {
				t.Errorf("parseStartCount(%q) = (%d,%d), want (%d,%d)",
					tt.token, start, count, tt.wantStart, tt.wantCount)
			}
		})
	}
}

// Test_parseHunkPlusRange verifies hunk-header parsing into inclusive ranges.
func Test_parseHunkPlusRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		wantStart int
		wantEnd   int
		wantOK    bool
	}{
		{name: "multi-line hunk", line: "@@ -1,0 +2,3 @@", wantStart: 2, wantEnd: 4, wantOK: true},
		{name: "single-line hunk", line: "@@ -1 +5 @@ func x()", wantStart: 5, wantEnd: 5, wantOK: true},
		{name: "pure deletion hunk", line: "@@ -3,2 +2,0 @@", wantOK: false},
		{name: "sub-1 start rejected", line: "@@ -1,0 +0,1 @@", wantOK: false},
		{name: "malformed", line: "@@ garbage", wantOK: false},
		{name: "deletion hunk with +word in function context", line: "@@ -3,5 +0,0 @@ +Constant", wantOK: false},
		{name: "missing +side never borrows a context +word", line: "@@ -3,5 @@ +12,4", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rng, ok := parseHunkPlusRange(tt.line)
			//: The ok flag gates the range.
			if ok != tt.wantOK {
				t.Fatalf("parseHunkPlusRange(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			//: Bounds only meaningful when ok.
			if ok && (rng.Start != tt.wantStart || rng.End != tt.wantEnd) {
				t.Errorf("parseHunkPlusRange(%q) = [%d,%d], want [%d,%d]",
					tt.line, rng.Start, rng.End, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

// Test_plusToken verifies the +side scan is confined to the range region
// between the two "@@" sentinels, so a function-context word starting with '+'
// after the closing "@@" is never picked up as a range token.
func Test_plusToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fields []string
		want   string
		wantOK bool
	}{
		{name: "well-formed header", fields: []string{"@@", "-1,2", "+3,4", "@@"}, want: "3,4", wantOK: true},
		{name: "real +token wins over later context", fields: []string{"@@", "-3,5", "+0,0", "@@", "+Constant"}, want: "0,0", wantOK: true},
		{name: "context +word after sentinel is never picked", fields: []string{"@@", "-3,5", "@@", "+12,4"}, wantOK: false},
		{name: "no +token in range region", fields: []string{"@@", "-1,2", "@@"}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, ok := plusToken(tt.fields)
			//: The ok flag is the primary contract.
			if ok != tt.wantOK {
				t.Fatalf("plusToken(%v) ok = %v, want %v", tt.fields, ok, tt.wantOK)
			}
			//: The token is only meaningful when ok.
			if ok && token != tt.want {
				t.Errorf("plusToken(%v) = %q, want %q", tt.fields, token, tt.want)
			}
		})
	}
}

// Test_parseNameStatus verifies the NUL-separated membership parser across
// single-path records, two-path rename records, exotic filenames, scope
// filtering, and truncated payloads.
func Test_parseNameStatus(t *testing.T) {
	t.Parallel()

	const root = "/repo"
	//: includeAll is the nil-equivalent: every path stays in the set.
	includeAll := func(string) bool { return true }
	//: excludeAll is the old suite's allGen, read the other way round — it is
	//: what a caller passes to keep nothing, and it stands in for the
	//: generated-file probe the source implementation hard-coded.
	excludeAll := func(string) bool { return false }
	//: onlyGo stands in for the ".go" suffix test the source implementation
	//: hard-coded, now supplied by the caller.
	onlyGo := func(path string) bool { return strings.HasSuffix(path, ".go") }

	tests := []struct {
		name    string
		payload string
		include IncludeFunc
		check   func(t *testing.T, set *ChangedSetValue)
	}{
		{
			name:    "modified accented file is file-touched",
			payload: "M\x00café.go\x00",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The non-ASCII path arrives verbatim and is file-touched.
				if !set.ContainsFile(filepath.Join(root, "café.go")) {
					t.Error("accented file not file-touched")
				}
			},
		},
		{
			name:    "rename marks both sides without lines",
			payload: "R100\x00old name.go\x00new name.go\x00",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The rename target (space in the name) is file-touched...
				if !set.ContainsFile(filepath.Join(root, "new name.go")) {
					t.Error("rename target not file-touched")
				}
				//: ...the old side keeps its package touched...
				if !set.ContainsDir(root) {
					t.Error("rename did not touch the package dir")
				}
				//: ...and membership-only parsing records no line ranges.
				if set.ContainsLine(filepath.Join(root, "new name.go"), 1) {
					t.Error("name-status parsing must not record line ranges")
				}
			},
		},
		{
			//: The source implementation hard-coded this exclusion. It is now
			//: the caller's, and the assertion is unchanged: given the filter a
			//: Go tool passes, a changed README must not populate the set.
			name:    "a caller's suffix filter keeps a non-Go path out of scope",
			payload: "M\x00README.md\x00",
			include: onlyGo,
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: A non-Go change must not populate the set.
				if !set.IsEmpty() {
					t.Errorf("non-Go path leaked into changed-set: %+v", set)
				}
			},
		},
		{
			//: The other half of moving that policy out: with no filter, the
			//: same payload IS in the set. A package that still dropped it
			//: would be deciding for callers who never asked.
			name:    "with no filter the same non-Go path is in scope",
			payload: "M\x00README.md\x00",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: nil/includeAll admits every path, whatever its suffix.
				if !set.ContainsFile(filepath.Join(root, "README.md")) {
					t.Errorf("unfiltered non-Go path missing from changed-set: %+v", set)
				}
			},
		},
		{
			name:    "generated file stays out of scope",
			payload: "M\x00gen.go\x00",
			include: excludeAll,
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: A generated file must not populate the set.
				if !set.IsEmpty() {
					t.Errorf("generated file leaked into changed-set: %+v", set)
				}
			},
		},
		{
			name:    "truncated rename record stops without panic",
			payload: "R100\x00only-old.go",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: A truncated record is dropped rather than misattributed.
				if !set.IsEmpty() {
					t.Errorf("truncated rename record misattributed: %+v", set)
				}
			},
		},
		{
			name:    "truncated single-path record stops without panic",
			payload: "M",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: A status with no path contributes nothing.
				if !set.IsEmpty() {
					t.Errorf("truncated record misattributed: %+v", set)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Default to admitting every path unless the row overrides.
			include := includeAll
			if tt.include != nil {
				include = tt.include
			}
			set := NewChangedSetValue(root)
			parseNameStatus(tt.payload, root, set, include)
			tt.check(t, set)
		})
	}
}

// Test_parseUnifiedDiff verifies full-payload parsing into a changed-set across
// modification, addition, deletion, rename, and generated-exclusion.
func Test_parseUnifiedDiff(t *testing.T) {
	t.Parallel()

	const root = "/repo"
	//: includeAll is the nil-equivalent: every path stays in the set.
	includeAll := func(string) bool { return true }
	//: excludeAll is the old suite's allGen, read the other way round — it is
	//: what a caller passes to keep nothing, and it stands in for the
	//: generated-file probe the source implementation hard-coded.
	excludeAll := func(string) bool { return false }

	tests := []struct {
		name    string
		diff    string
		include IncludeFunc
		check   func(t *testing.T, set *ChangedSetValue)
	}{
		{
			name:    "generated target is excluded",
			diff:    "diff --git a/g.go b/g.go\n--- a/g.go\n+++ b/g.go\n@@ -0,0 +1,1 @@\n+x\n",
			include: excludeAll,
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: A generated target contributes nothing to the set.
				if !set.IsEmpty() {
					t.Errorf("generated target should be excluded, got %+v", set)
				}
			},
		},
		{
			name: "modification records the +side range",
			diff: "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -2,0 +3,2 @@\n+a\n+b\n",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: Lines 3 and 4 are in the diff, 2 is not.
				if !set.ContainsLine(filepath.Join(root, "foo.go"), 3) || !set.ContainsLine(filepath.Join(root, "foo.go"), 4) {
					t.Error("expected lines 3-4 in diff")
				}
				if set.ContainsLine(filepath.Join(root, "foo.go"), 2) {
					t.Error("line 2 should not be in diff")
				}
			},
		},
		{
			name: "deletion touches package but records no lines",
			diff: "diff --git a/foo.go b/foo.go\ndeleted file mode 100644\n--- a/foo.go\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-a\n-b\n",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The package dir is touched...
				if !set.ContainsDir(root) {
					t.Error("deletion should touch package dir")
				}
				//: ...but no lines are recorded.
				if set.ContainsLine(filepath.Join(root, "foo.go"), 1) {
					t.Error("deletion should not record changed lines")
				}
			},
		},
		{
			name: "pure rename touches file without lines",
			diff: "diff --git a/old.go b/new.go\nsimilarity index 100%\nrename from old.go\nrename to new.go\n",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The new path is file-touched...
				if !set.ContainsFile(filepath.Join(root, "new.go")) {
					t.Error("rename target should be file-touched")
				}
				//: ...with no changed lines.
				if set.ContainsLine(filepath.Join(root, "new.go"), 1) {
					t.Error("pure rename should record no lines")
				}
			},
		},
		{
			//: A path containing a space gets a trailing TAB delimiter on its
			//: "---"/"+++" headers ("+++ b/with space.go\t"); the parser must
			//: strip it or the ".go" suffix check silently drops the ranges.
			name: "spaced path with trailing header tab keeps its range",
			diff: "diff --git a/with space.go b/with space.go\n--- a/with space.go\t\n+++ b/with space.go\t\n@@ -2,0 +3,1 @@\n+x\n",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The spaced path keeps its +side line range.
				if !set.ContainsLine(filepath.Join(root, "with space.go"), 3) {
					t.Error("spaced path lost its line range to the header tab")
				}
			},
		},
		{
			//: Under -U0 a deleted source line whose content begins with
			//: "-- " arrives as "--- a/other/evil.go". A naive parser reads
			//: that hunk-body line as a "--- " pre-edit path header and falsely
			//: marks /repo/other as touched. Gating path parsing to the
			//: pre-hunk region keeps the changed-set exact.
			name: "hunk body lines are not misparsed as path headers",
			diff: "diff --git a/foo.go b/foo.go\n--- a/foo.go\n+++ b/foo.go\n@@ -1,1 +1,1 @@\n-- a/other/evil.go\n+normal\n",
			check: func(t *testing.T, set *ChangedSetValue) {
				t.Helper()
				//: The real +side line of foo.go is still recorded.
				if !set.ContainsLine(filepath.Join(root, "foo.go"), 1) {
					t.Error("expected line 1 of foo.go in diff")
				}
				//: The deleted body line must NOT touch a spurious package.
				if set.ContainsDir(filepath.Join(root, "other")) {
					t.Error("hunk body line misparsed as --- path header (false-positive package)")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: Default to admitting every path unless the row overrides.
			include := includeAll
			if tt.include != nil {
				include = tt.include
			}
			set := NewChangedSetValue(root)
			parseUnifiedDiff(tt.diff, root, set, include)
			tt.check(t, set)
		})
	}
}
