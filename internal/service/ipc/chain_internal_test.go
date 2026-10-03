//go:build unix

package ipc

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/pathchain"
)

// componentInfo is an lstat answer as a Unix kernel gives one — naming the
// component's owner — or, with named false, one the platform does not
// describe with a Stat_t.
type componentInfo struct {
	mode  fs.FileMode
	uid   int
	named bool
}

// Name is the component's name; the rule never reads it.
func (i componentInfo) Name() string { return "app" }

// Size is irrelevant to the rule.
func (i componentInfo) Size() int64 { return 0 }

// Mode is the component's own mode.
func (i componentInfo) Mode() fs.FileMode { return i.mode }

// ModTime is irrelevant to the rule.
func (i componentInfo) ModTime() time.Time { return time.Time{} }

// IsDir reports what the mode says.
func (i componentInfo) IsDir() bool { return i.mode.IsDir() }

// Sys is where a Unix kernel names the owner, absent when named is false.
func (i componentInfo) Sys() any {
	if !i.named {
		return nil
	}
	return &syscall.Stat_t{Uid: uint32(i.uid)}
}

// TestSteerable pins chain_unix.go's rule on synthetic steps, owners included
// — another account's and root's, which an unprivileged test cannot create on
// a real tree, and one nobody can read, which a gate must not guess about.
func TestSteerable(t *testing.T) {
	const self, other int = 1000, 2000
	dir := os.ModeDir | 0o700
	link := os.ModeSymlink | 0o777
	sticky := os.ModeDir | os.ModeSticky | 0o777
	for _, c := range []struct {
		name      string
		container fs.FileMode
		info      fs.FileInfo
		want      string // "" when no account the deployment did not choose can steer it
	}{
		{"another account's directory where only its owner writes", os.ModeDir | 0o755, componentInfo{dir, other, true}, ""},
		{"another account's link where a group writes", os.ModeDir | 0o770, componentInfo{link, other, true}, ""},
		{"our link where anybody writes", os.ModeDir | 0o777, componentInfo{link, self, true}, kindIndirection},
		{"root's link where anybody writes, sticky or not", sticky, componentInfo{link, 0, true}, kindIndirection},
		{"our directory in /tmp's shape", sticky, componentInfo{dir, self, true}, ""},
		{"root's directory in /tmp's shape", sticky, componentInfo{dir, 0, true}, ""},
		{"another account's directory in /tmp's shape", sticky, componentInfo{dir, other, true}, kindForeign},
		{"our directory where anybody writes, without sticky", os.ModeDir | 0o777, componentInfo{dir, self, true}, kindReplaceable},
		{"our directory where others may only write", os.ModeDir | 0o702, componentInfo{dir, self, true}, kindReplaceable},
		{"another account's directory, without sticky: who made it comes first", os.ModeDir | 0o777, componentInfo{dir, other, true}, kindForeign},
		{"a directory whose owner cannot be read", sticky, componentInfo{dir, self, false}, kindForeign},
		{"a step that carries no lstat answer", sticky, nil, kindForeign},
	} {
		step := pathchain.StepValue{Path: "/pub/app", Name: "app", Container: c.container, Info: c.info}
		if c.info != nil {
			step.Mode = c.info.Mode()
			step.Indirect = c.info.Mode()&fs.ModeSymlink != 0
		}
		if got, steered := steerable(&step, self); got != c.want || steered != (c.want != "") {
			t.Errorf("%s: steerable = (%q, %v), want %q", c.name, got, steered, c.want)
		}
	}
}
