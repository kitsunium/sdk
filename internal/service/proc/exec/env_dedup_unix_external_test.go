//go:build unix

package exec_test

import (
	"slices"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// envProgram prints the environment it was executed with, one entry a line, as
// the kernel handed it: a shell rebuilds its own and os.Environ drops a
// duplicate, so neither would show one.
const envProgram string = "/usr/bin/env"

// spawnRoute is one way a spec reaches its program.
type spawnRoute struct {
	name  string
	apply func(*coreproc.Spec)
}

// spawnRoutes are the spawn's two paths: straight to the program, and through
// the re-exec trampoline, which an rlimit forces.
var spawnRoutes = []spawnRoute{
	{"direct", func(*coreproc.Spec) {}},
	{"trampoline", func(spec *coreproc.Spec) {
		spec.Rlimits = map[coreproc.Resource]coreproc.LimitValue{coreproc.ResourceNoFile: {Soft: 256, Hard: 256}}
	}},
}

// environOf runs spec, stdout captured, and returns the lines its program
// printed.
func environOf(t *testing.T, spec coreproc.Spec) []string {
	t.Helper()
	var out strings.Builder
	spec.Stdio, spec.Stdout = coreproc.StdioCapture, &out
	p, err := svcexec.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start = %v, want nil", spawnDetail(err))
	}
	exit, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if !exit.Success() {
		t.Fatalf("the program exited %d", exit.Code)
	}
	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

// entriesOf keeps the lines that assign name.
func entriesOf(lines []string, name string) []string {
	var out []string
	//: each entry the child was handed.
	for _, line := range lines {
		//: this name.
		if strings.HasPrefix(line, name+"=") {
			out = append(out, line)
		}
	}
	//: as many as it holds.
	return out
}

// TestStartEachVariableOnce: a name set more than once in Spec.Env reaches the
// child once, with its last value, at the place of its last occurrence — on
// both of the spawn's paths. append(os.Environ(), "K=v") is os/exec's documented
// way to replace a variable, and a child's getenv reads the first entry it
// finds, so handing both on made the override a no-op. 2026-11-08: a supervisor
// test's child read the supervisor's role that way and supervised in turn.
func TestStartEachVariableOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		env  []string
		want []string
	}
	tests := []tc{
		{"an override replaces what it follows", []string{"A=1", "KEEP=k", "A=2"}, []string{"KEEP=k", "A=2"}},
		{"three of one name keep the last", []string{"A=1", "A=2", "A=3"}, []string{"A=3"}},
		{"distinct names keep their order", []string{"B=1", "A=2"}, []string{"B=1", "A=2"}},
	}
	runCase := func(t *testing.T, route spawnRoute, c tc) {
		t.Helper()
		spec := coreproc.Spec{Path: envProgram, Args: []string{"env"}, Env: c.env}
		route.apply(&spec)
		if got := environOf(t, spec); !slices.Equal(got, c.want) {
			t.Errorf("the child was handed %q, want %q", got, c.want)
		}
	}
	for _, route := range spawnRoutes {
		for _, c := range tests {
			t.Run(route.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				runCase(t, route, c)
			})
		}
	}
}

// TestStartSearchesThePathTheChildReads: the PATH a bare name is searched in is
// the one the child is handed. searchPathOf reads Spec.Env's last PATH; a
// spawn that handed the child the first ran a program its own PATH does not
// name.
func TestStartSearchesThePathTheChildReads(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, route spawnRoute) {
		t.Helper()
		// The first PATH is an empty directory: a search of it finds nothing.
		spec := coreproc.Spec{Path: "env", Env: []string{"PATH=" + t.TempDir(), "PATH=/usr/bin"}}
		route.apply(&spec)
		if got := entriesOf(environOf(t, spec), "PATH"); !slices.Equal(got, []string{"PATH=/usr/bin"}) {
			t.Errorf("the child read PATH as %q, want the one searched, %q", got, "PATH=/usr/bin")
		}
	}
	for _, route := range spawnRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, route)
		})
	}
}
