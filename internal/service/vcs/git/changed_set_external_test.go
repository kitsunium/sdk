// Package git_test black-box tests the public changed-set query API. The
// query methods report false on a freshly constructed (empty) set — the
// clean-branch shape consumers like pkg/serve rely on; populated-set behaviour
// is covered white-box in changed_set_value_internal_test.go.
package git_test

import (
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// TestNewChangedSetValue verifies a fresh set is empty.
func TestNewChangedSetValue(t *testing.T) {
	t.Parallel()
	//: A freshly constructed set has touched nothing.
	if !gitpkg.NewChangedSetValue("/repo").IsEmpty() {
		t.Error("new changed-set should be empty")
	}
}

// TestChangedSetValue_IsEmpty verifies IsEmpty on a fresh set.
func TestChangedSetValue_IsEmpty(t *testing.T) {
	t.Parallel()
	//: Nothing added → empty.
	if !gitpkg.NewChangedSetValue("/repo").IsEmpty() {
		t.Error("IsEmpty should be true for a new set")
	}
}

// TestChangedSetValue_ContainsLine verifies the empty-set line query is false.
func TestChangedSetValue_ContainsLine(t *testing.T) {
	t.Parallel()
	//: An empty set contains no changed line.
	if gitpkg.NewChangedSetValue("/repo").ContainsLine("/repo/foo.go", 1) {
		t.Error("empty set should not contain a line")
	}
}

// TestChangedSetValue_ContainsFile verifies the empty-set file query is false.
func TestChangedSetValue_ContainsFile(t *testing.T) {
	t.Parallel()
	//: An empty set contains no file.
	if gitpkg.NewChangedSetValue("/repo").ContainsFile("/repo/foo.go") {
		t.Error("empty set should not contain a file")
	}
}

// TestChangedSetValue_ContainsDir verifies the empty-set package query is
// false.
func TestChangedSetValue_ContainsDir(t *testing.T) {
	t.Parallel()
	//: An empty set touches no package dir.
	if gitpkg.NewChangedSetValue("/repo").ContainsDir("/repo") {
		t.Error("empty set should not contain a package dir")
	}
}
