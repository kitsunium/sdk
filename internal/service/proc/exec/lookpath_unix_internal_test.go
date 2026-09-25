//go:build unix

package exec

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// writeProgram writes a file named name in dir with mode, and returns its path.
func writeProgram(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

// Test_resolveSpec pins where a bare name is searched — the child's PATH when
// Spec.Env carries one, the parent's otherwise — and os/exec's rules on the
// way: the first executable in PATH order wins, a non-executable file or a
// directory is skipped, a match through a relative entry is refused with
// exec.ErrDot, and a name found nowhere is exec.ErrNotFound. It sets PATH and
// changes directory, so it cannot run in parallel.
func Test_resolveSpec(t *testing.T) {
	envDir, parentDir, shadowDir := t.TempDir(), t.TempDir(), t.TempDir()
	inEnv := writeProgram(t, envDir, "kprobe-env", 0o755)
	inParent := writeProgram(t, parentDir, "kprobe-parent", 0o755)
	writeProgram(t, parentDir, "kprobe-env", 0o755)
	writeProgram(t, shadowDir, "kprobe-parent", 0o644)
	if err := os.Mkdir(filepath.Join(shadowDir, "kprobe-dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeProgram(t, parentDir, "kprobe-dir", 0o755)
	t.Setenv("PATH", shadowDir+string(os.PathListSeparator)+parentDir)
	type tc struct {
		name     string
		spec     coreproc.Spec
		wantPath string
		wantArgs []string
		wantErr  error
	}
	tests := []tc{
		{
			"Spec.Env's PATH is searched first — it is the child's",
			coreproc.Spec{Path: "kprobe-env", Env: []string{"PATH=" + envDir}},
			inEnv,
			[]string{"kprobe-env"},
			nil,
		},
		{
			"without a PATH in Spec.Env, the parent's is searched",
			coreproc.Spec{Path: "kprobe-parent", Env: []string{"HOME=/nowhere"}},
			inParent,
			[]string{"kprobe-parent"},
			nil,
		},
		{
			"a nil Spec.Env searches the parent's PATH",
			coreproc.Spec{Path: "kprobe-parent"},
			inParent,
			[]string{"kprobe-parent"},
			nil,
		},
		{
			"the last PATH in Spec.Env wins, as os/exec reads a duplicate",
			coreproc.Spec{Path: "kprobe-env", Env: []string{"PATH=/nowhere", "PATH=" + envDir}},
			inEnv,
			[]string{"kprobe-env"},
			nil,
		},
		{
			"a non-executable file and a directory are skipped",
			coreproc.Spec{Path: "kprobe-dir"},
			filepath.Join(parentDir, "kprobe-dir"),
			[]string{"kprobe-dir"},
			nil,
		},
		{
			"explicit Args are kept",
			coreproc.Spec{Path: "kprobe-parent", Args: []string{"custom-argv0", "x"}},
			inParent,
			[]string{"custom-argv0", "x"},
			nil,
		},
		{
			"a path is left as written",
			coreproc.Spec{Path: "./bin/tool"},
			"./bin/tool", nil, nil,
		},
		{
			"a name found nowhere",
			coreproc.Spec{Path: "kprobe-missing", Env: []string{"PATH=" + envDir}},
			"", nil, osexec.ErrNotFound,
		},
		{
			"a match through a relative entry is refused",
			coreproc.Spec{Path: "kprobe-env", Env: []string{"PATH=."}},
			"", nil, osexec.ErrDot,
		},
		{
			"an empty entry is the current directory, and refused the same way",
			coreproc.Spec{Path: "kprobe-env", Env: []string{"PATH=" + string(os.PathListSeparator) + "/nowhere"}},
			"", nil, osexec.ErrDot,
		},
	}
	t.Chdir(envDir)
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := resolveSpec(c.spec)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) || !errs.HasCode(err, coreproc.CodeSpawnFailed) {
				t.Fatalf("%s: resolveSpec = %v, want SPAWN_FAILED wrapping %v", c.name, err, c.wantErr)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: resolveSpec = %v", c.name, err)
		}
		if got.Path != c.wantPath || strings.Join(got.Args, " ") != strings.Join(c.wantArgs, " ") {
			t.Errorf("%s: resolved (%q, %q), want (%q, %q)", c.name, got.Path, got.Args, c.wantPath, c.wantArgs)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
