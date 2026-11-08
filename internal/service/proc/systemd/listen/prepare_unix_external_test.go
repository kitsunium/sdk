//go:build unix

package listen_test

import (
	"errors"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
	"github.com/kitsunium/sdk/internal/service/proc/systemd/listen"
)

// TestPrepareRefusesInvalidNames pins #739: a name LISTEN_FDNAMES cannot
// carry is InvalidFDName before the Spec changes or a socket is duplicated;
// the empty name is announced as "unknown".
func TestPrepareRefusesInvalidNames(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	spec := coreproc.Spec{Path: "/bin/true", Env: []string{"A=1"}}
	if pErr := listen.Prepare(&spec, map[string]net.Listener{"a:b": ln}); !errors.Is(pErr, coreproc.InvalidFDName) {
		t.Fatalf("Prepare(a:b) = %v, want InvalidFDName", pErr)
	}
	if len(spec.ExtraFiles) != 0 || !slices.Equal(spec.Env, []string{"A=1"}) || len(spec.PIDEnv) != 0 {
		t.Errorf("a refused Prepare changed the Spec: %+v", spec)
	}
	if pErr := listen.Prepare(&spec, map[string]net.Listener{"": ln}); pErr != nil {
		t.Fatalf("Prepare(empty name) = %v", pErr)
	}
	defer closeExtra(t, spec.ExtraFiles)
	if !envHas(spec.Env, "LISTEN_FDNAMES=unknown") {
		t.Errorf("Env = %v, want the empty name announced as unknown", spec.Env)
	}
}

// TestPrepareFilesHandsThemAsTheyAre pins #739's store half: the files go to
// fd 3 on in the order given, the very *os.File values — neither duplicated
// nor closed —, before any ExtraFiles the Spec had, their names announced.
func TestPrepareFilesHandsThemAsTheyAre(t *testing.T) {
	t.Parallel()
	a, b, earlier := pipeEnd(t), pipeEnd(t), pipeEnd(t)
	spec := coreproc.Spec{Path: "/bin/true", ExtraFiles: []*os.File{earlier}, PIDEnv: []string{"LISTEN_PID"}}
	files := []listen.NamedFileValue{{Name: "store-a", File: a}, {Name: "", File: b}}
	if err := listen.PrepareFiles(&spec, files); err != nil {
		t.Fatalf("PrepareFiles = %v", err)
	}
	if !slices.Equal(spec.ExtraFiles, []*os.File{a, b, earlier}) {
		t.Errorf("ExtraFiles = %v, want the two files as given, then the earlier one", spec.ExtraFiles)
	}
	if !envHas(spec.Env, "LISTEN_FDS=2") || !envHas(spec.Env, "LISTEN_FDNAMES=store-a:unknown") {
		t.Errorf("Env = %v", spec.Env)
	}
	if !slices.Equal(spec.PIDEnv, []string{"LISTEN_PID"}) {
		t.Errorf("PIDEnv = %q, want LISTEN_PID once", spec.PIDEnv)
	}
	bad := coreproc.Spec{Path: "/bin/true"}
	if err := listen.PrepareFiles(&bad, []listen.NamedFileValue{{Name: "x:y", File: a}}); !errors.Is(err, coreproc.InvalidFDName) || len(bad.ExtraFiles) != 0 {
		t.Errorf("PrepareFiles(x:y) = %v, Spec %+v; want InvalidFDName and no change", err, bad)
	}
}

// pipeEnd returns the read end of a pipe, both ends closed at the test's end.
func pipeEnd(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r
}

// TestPrepareFilesEndToEnd pins #739 in a real child: it reads LISTEN_FDS,
// LISTEN_FDNAMES and a LISTEN_PID that is its own — set after the fork, not
// the parent's —, holds exactly fds 0–4 on Linux (a scan of /proc/self/fd),
// and the parent's files are still open after the spawn.
func TestPrepareFilesEndToEnd(t *testing.T) {
	t.Parallel()
	a, b := pipeEnd(t), pipeEnd(t)
	scan := `fds=""; for n in $(seq 0 64); do [ -e /proc/self/fd/$n ] && fds="$fds $n"; done`
	if runtime.GOOS != "linux" {
		scan = `fds=" skipped"`
	}
	var out strings.Builder
	spec := coreproc.Spec{
		Path:   "/bin/sh",
		Args:   []string{"sh", "-c", scan + `; printf '%s|%s|%s|%s|%s' "$LISTEN_FDS" "$LISTEN_FDNAMES" "$LISTEN_PID" "$$" "$fds"`},
		Env:    []string{"PATH=/usr/bin:/bin", "LISTEN_PID=1"},
		Stdio:  coreproc.StdioCapture,
		Stdout: &out,
	}
	if err := listen.PrepareFiles(&spec, []listen.NamedFileValue{{Name: "web", File: a}, {File: b}}); err != nil {
		t.Fatalf("PrepareFiles = %v", err)
	}
	p, err := svcexec.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	if _, wErr := p.Wait(); wErr != nil {
		t.Fatalf("Wait = %v", wErr)
	}
	got := strings.Split(out.String(), "|")
	if len(got) != 5 {
		t.Fatalf("the child printed %q", out.String())
	}
	if got[0] != "2" || got[1] != "web:unknown" {
		t.Errorf("LISTEN_FDS=%q LISTEN_FDNAMES=%q, want 2 and web:unknown", got[0], got[1])
	}
	if got[2] != got[3] || got[2] == strconv.Itoa(os.Getpid()) {
		t.Errorf("LISTEN_PID=%q, the child is %s and the parent %d: want the child's own", got[2], got[3], os.Getpid())
	}
	if runtime.GOOS == "linux" && got[4] != " 0 1 2 3 4" {
		t.Errorf("the child holds fds%s, want 0 1 2 3 4 and nothing else", got[4])
	}
	for _, f := range []*os.File{a, b} {
		if _, sErr := f.Stat(); sErr != nil {
			t.Errorf("the parent's file %s was closed by the spawn: %v", f.Name(), sErr)
		}
	}
}
