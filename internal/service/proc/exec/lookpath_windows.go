//go:build windows

// Package exec — the Windows spellings of a PATH search: a name is tried with
// each PATHEXT extension unless it already carries one, a regular file is
// runnable by its extension, and variable names are case-insensitive ("Path"
// is the usual spelling there).
package exec

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// pathExtVariable lists the extensions a bare name is tried with.
const pathExtVariable string = "PATHEXT"

// defaultPathExt is what os/exec.LookPath uses when PATHEXT is unset.
const defaultPathExt string = ".com;.exe;.bat;.cmd"

// candidates is every spelling name could have in dir, in os/exec.LookPath's
// order: name itself first when it already carries an extension, then name
// with each PATHEXT extension, in PATHEXT order.
func candidates(dir, name string, env []string) []string {
	exts := pathExts(env)
	out := make([]string, 0, len(exts)+1)
	//: "go.exe" is tried as written before "go.exe.exe" is.
	if filepath.Ext(name) != "" {
		out = append(out, filepath.Join(dir, name))
	}
	//: each runnable extension, in the order PATHEXT gives.
	for _, ext := range exts {
		out = append(out, filepath.Join(dir, name+ext))
	}
	//: the spellings to try.
	return out
}

// pathExts is the PATHEXT list the child would see, else the parent's, else
// os/exec's default.
func pathExts(env []string) []string {
	value, _ := envValue(env, pathExtVariable)
	//: the child's environment names none, or names it empty.
	if value == "" {
		value = os.Getenv(pathExtVariable)
	}
	//: nobody names one.
	if value == "" {
		value = defaultPathExt
	}
	exts := make([]string, 0, strings.Count(value, ";")+1)
	//: every non-empty entry, lower-cased and dotted.
	for _, ext := range strings.Split(strings.ToLower(value), ";") {
		//: an empty entry adds nothing.
		if ext == "" {
			continue
		}
		//: "exe" and ".exe" name the same extension.
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		exts = append(exts, ext)
	}
	//: the extensions to try.
	return exts
}

// executableMode reports whether a regular file is runnable: on Windows the
// extension decides, and every candidate already carries a runnable one.
func executableMode(_ fs.FileMode) bool {
	//: the mode says nothing about running here.
	return true
}

// isPath reports whether name is a file path rather than a command name: on
// Windows a colon, a backslash or a slash makes it one, as
// os/exec.LookPath decides ("C:go.exe" names a drive-relative file).
func isPath(name string) bool {
	//: any of the three separators.
	return strings.ContainsAny(name, `:\/`)
}

// sameVariable compares two environment variable names; Windows names are
// case-insensitive.
func sameVariable(left, right string) bool {
	//: "Path" and "PATH" are one variable.
	return strings.EqualFold(left, right)
}
