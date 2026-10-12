package kit_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A product that imports framework/kit alone initialises no subsystem it did
// not import (ADR 0172): neither the HTTP engine and the frontends' file server
// (framework/kit/server), nor the Studio's event stream and profiler
// (framework/kit/studio), nor the process sampler — runtime/metrics, whose
// initialisation every process importing it pays, and the spawn engine behind
// pkg/v1/proc/process. Go runs the initialisation of every package a binary
// imports, used or not, so the import graph is what decides.
//
// It lists the graph with the go command, and skips without one — under
// Bazel's sandbox. Its gate is CI's test-386 job, which runs go test over
// every module with the toolchain on PATH (rule 12).
func TestTheFacadeImportsNoOptInSubsystem(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain to list the import graph")
	}
	list := exec.Command(goTool, "list", "-deps", "github.com/kitsunium/sdk/framework/kit")
	list.Env = append(os.Environ(), "GOWORK=off")
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list -deps framework/kit: %v", err)
	}
	deps := make(map[string]bool)
	for _, p := range strings.Fields(string(out)) {
		deps[p] = true
	}
	if !deps["github.com/kitsunium/sdk/framework/internal/kit"] {
		t.Fatalf("the listing does not hold framework/internal/kit:\n%s", out)
	}
	for _, opt := range []struct{ pkg, by string }{
		{"github.com/kitsunium/sdk/pkg/v1/net/server", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/static", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/proc/process", "framework/kit/server"},
		{"github.com/kitsunium/sdk/internal/service/proc/exec", "framework/kit/server"},
		{"runtime/metrics", "framework/kit/server"},
		{"github.com/kitsunium/sdk/pkg/v1/net/sse", "framework/kit/studio"},
		{"github.com/kitsunium/sdk/pkg/v1/observe/profiling", "framework/kit/studio"},
	} {
		if deps[opt.pkg] {
			t.Errorf("framework/kit imports %s, which only %s may bring", opt.pkg, opt.by)
		}
	}
}
