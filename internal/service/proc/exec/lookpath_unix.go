//go:build unix

// Package exec — the Unix spellings of a PATH search: one candidate per
// directory, executable when any execute bit is set, variable names
// case-sensitive.
package exec

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// anyExecuteBit is the owner, group and other execute bits. os/exec.LookPath
// accepts a file carrying any of them; so does this search.
const anyExecuteBit fs.FileMode = 0o111

// candidates is the one file name could be in dir.
func candidates(dir, name string, _ []string) []string {
	//: Unix runs a file by its exact name.
	return []string{filepath.Join(dir, name)}
}

// executableMode reports whether a regular file's mode lets something run it.
func executableMode(mode fs.FileMode) bool {
	//: any execute bit, as os/exec.LookPath decides.
	return mode&anyExecuteBit != 0
}

// isPath reports whether name is a file path rather than a command name: on
// Unix, any slash makes it one, as os/exec.LookPath decides.
func isPath(name string) bool {
	//: "./tool" and "bin/tool" are paths; "tool" is a name.
	return strings.Contains(name, "/")
}

// sameVariable compares two environment variable names; Unix names are
// case-sensitive.
func sameVariable(left, right string) bool {
	//: byte equality.
	return left == right
}
