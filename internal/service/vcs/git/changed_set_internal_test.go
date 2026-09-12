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
