// Package git — the two filesystem fixtures the white-box tests share.
package git

import (
	"os"
	"testing"
)

// mkdirT creates dir and every parent, failing the test rather than the case.
func mkdirT(t *testing.T, dir string) {
	t.Helper()
	//: A fixture that cannot be built invalidates the row, not the rule.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// symlinkT links newname at oldname, skipping where the platform refuses —
// unprivileged symbolic links are not available on every Windows host.
func symlinkT(t *testing.T, oldname, newname string) {
	t.Helper()
	//: A platform that cannot make the link cannot exhibit the defect either.
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
}
