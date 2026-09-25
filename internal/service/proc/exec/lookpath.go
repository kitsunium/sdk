//go:build unix || windows

// Package exec — resolving a bare executable name through the PATH the child
// will run with.
//
// os.StartProcess does not search PATH: it hands its path to execve (or to
// CreateProcess) as written, so a bare "go" names a file in the current
// directory and fails with ENOENT. The port documents Spec.Path as "absolute
// or PATH-resolvable", which is what os/exec.Command gives a Go programmer, so
// the resolution happens here, once, before either spawn path — the direct
// fork/exec and the limits trampoline, which execs the target itself.
package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pathVariable is the environment variable listing the directories searched.
const pathVariable string = "PATH"

// resolveSpec returns spec with Path resolved to the executable the spawn
// runs, and — when the caller gave no Args — argv defaulted to the name as the
// caller WROTE it, which is what os/exec puts in argv[0] and what a program
// reading its own name expects.
//
// A Path containing a separator is a file path and is left as written: a
// relative one is resolved by the kernel against Spec.Dir, as os/exec
// documents. A bare name is searched in the PATH the CHILD will see — the PATH
// entry of Spec.Env when it carries one (the last, as os/exec reads a
// duplicated key), the parent's otherwise, including when Spec.Env is nil and
// the child's environment is empty. A match found through a relative PATH
// entry ("." or an empty one) is refused, which is Go 1.19's exec.ErrDot rule:
// running whatever file happens to sit in the working directory under a
// command's name is how a checked-out repository executes code nobody chose.
func resolveSpec(spec coreproc.Spec) (resolved coreproc.Spec, err error) {
	//: a path, not a name: the kernel resolves it, relative to Dir if needed.
	if isPath(spec.Path) {
		//: unchanged.
		return spec, nil
	}
	path, lookErr := lookPath(spec.Path, searchPathOf(spec.Env), spec.Env)
	//: not found, or found only through a relative PATH entry.
	if lookErr != nil {
		//: SPAWN_FAILED, with os/exec's own sentinel in the chain.
		return spec, wrapSpawn(lookErr, errs.String("path", spec.Path),
			errs.Bool("from_spec_env", envValue(spec.Env, pathVariable) != ""))
	}
	//: argv[0] keeps the name the caller wrote.
	if len(spec.Args) == 0 {
		spec.Args = []string{spec.Path}
	}
	spec.Path = path
	//: the spec the spawn runs.
	return spec, nil
}

// searchPathOf returns the PATH a bare name is searched in: the child's own
// when its environment names one, the parent's otherwise.
func searchPathOf(env []string) string {
	//: the child's PATH is the one the program would have seen.
	if value := envValue(env, pathVariable); value != "" {
		//: from Spec.Env.
		return value
	}
	//: the parent's, as os/exec.Command searches.
	return os.Getenv(pathVariable)
}

// envValue returns the last value of key in env, compared as the platform
// compares variable names.
func envValue(env []string, key string) string {
	value := ""
	//: the last entry wins, as os/exec reads a duplicated key.
	for _, entry := range env {
		name, entryValue, found := strings.Cut(entry, "=")
		//: a match under this platform's rule for variable names.
		if found && sameVariable(name, key) {
			value = entryValue
		}
	}
	//: empty when the key is absent.
	return value
}

// lookPath searches name in every directory of searchPath, in order, and
// returns the first executable candidate. A candidate found through a relative
// directory is refused with exec.ErrDot rather than skipped, exactly as
// os/exec.LookPath refuses it: skipping would silently run a DIFFERENT program
// from the one the PATH order selects.
func lookPath(name, searchPath string, env []string) (path string, err error) {
	//: each PATH entry, in order; an empty one means the current directory.
	for _, dir := range filepath.SplitList(searchPath) {
		//: an empty entry is ".", as every shell reads it.
		if dir == "" {
			dir = "."
		}
		//: the platform's spellings of name in dir (PATHEXT on Windows).
		for _, candidate := range candidates(dir, name, env) {
			//: not a runnable file here; the next spelling or directory may be.
			if !isExecutable(candidate) {
				continue
			}
			//: found, but only relative to wherever the process happens to be.
			if !filepath.IsAbs(candidate) {
				//: os/exec's own ErrDot, so errors.Is answers as it does there.
				return "", &osexec.Error{Name: name, Err: osexec.ErrDot}
			}
			//: the executable.
			return candidate, nil
		}
	}
	//: os/exec's own ErrNotFound.
	return "", &osexec.Error{Name: name, Err: osexec.ErrNotFound}
}

// isExecutable reports whether path is a regular file this platform would
// run: any execute bit on Unix, any existing regular file on Windows (where
// the extension, not a mode, decides).
func isExecutable(path string) bool {
	info, statErr := os.Stat(path)
	//: absent, unreadable, a directory or a device: not a program.
	if statErr != nil || !info.Mode().IsRegular() {
		//: skip it.
		return false
	}
	//: the platform's rule.
	return executableMode(info.Mode())
}
