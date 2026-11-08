//go:build unix

package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// sealProbeEnv makes the test binary run as the probe child of
// TestStart_sealsTheFDTable: it reports which descriptors it inherited.
const sealProbeEnv string = "SDK_EXEC_SEAL_PROBE"

// sealProbeFDsEnv carries the descriptor numbers the probe checks, comma
// separated.
const sealProbeFDsEnv string = "SDK_EXEC_SEAL_PROBE_FDS"

// TestStart_sealsTheFDTable holds a descriptor without close-on-exec — a dup
// of a pipe end, as a systemd activation socket or a leak from the shell that
// started the supervisor is — and spawns a probe child, this test binary,
// that fstat's it. Sealed by default, the child must not hold it; with
// FDsInherited it must, as before #1089; and the parent still reads and writes
// it after the spawn, since the seal sets a flag and closes nothing. The
// ExtraFiles of the same spawns still land at fd 3 and 4, readable, with and
// without the trampoline. It runs alone: the seal is process-wide, and a
// parallel test's spawn would seal the descriptor before the FDsInherited row.
func TestStart_sealsTheFDTable(t *testing.T) {
	//: the probe half: report the descriptors and leave.
	if os.Getenv(sealProbeEnv) != "" {
		runSealProbe(t)
		return
	}
	type tc struct {
		name       string
		inherit    bool
		trampoline bool
		wantLeaked bool
		wantExtras string
	}
	tests := []tc{
		{"sealed by default", false, false, false, "ab"},
		{"sealed through the trampoline", false, true, false, "ab"},
		{"FDsInherited keeps today's table", true, false, true, "ab"},
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe = %v", err)
		}
		defer pr.Close()
		defer pw.Close()
		//: syscall.Dup leaves the new descriptor without FD_CLOEXEC.
		leaked, err := syscall.Dup(int(pw.Fd()))
		if err != nil {
			t.Fatalf("Dup = %v", err)
		}
		defer syscall.Close(leaked)
		extraA, extraB := payloadPipe(t, "a"), payloadPipe(t, "b")
		var out strings.Builder
		spec := coreproc.Spec{
			Path:       self,
			Args:       []string{self, "-test.run=^TestStart_sealsTheFDTable$", "-test.count=1"},
			Env:        []string{sealProbeEnv + "=1", sealProbeFDsEnv + "=" + strconv.Itoa(leaked)},
			ExtraFiles: []*os.File{extraA, extraB},
			FDs:        fdPolicy(c.inherit),
			Stdio:      coreproc.StdioCapture,
			Stdout:     &out,
		}
		//: a umask routes the spawn through the trampoline's own execve.
		if c.trampoline {
			spec.Umask = new(0o022)
		}
		p, err := Start(t.Context(), spec)
		extraA.Close()
		extraB.Close()
		if err != nil {
			t.Fatalf("Start = %v", err)
		}
		if _, err := p.Wait(); err != nil {
			t.Fatalf("Wait = %v", err)
		}
		report := out.String()
		if got := strings.Contains(report, "leaked=open"); got != c.wantLeaked {
			t.Errorf("child holds fd %d = %v, want %v (report %q)", leaked, got, c.wantLeaked, report)
		}
		if !strings.Contains(report, "extras="+c.wantExtras) {
			t.Errorf("ExtraFiles at fd 3 and 4 read %q, want extras=%s", report, c.wantExtras)
		}
		//: the parent's descriptor is untouched: a write through it reaches pr.
		if _, err := syscall.Write(leaked, []byte{'z'}); err != nil {
			t.Fatalf("writing through fd %d after the spawn = %v", leaked, err)
		}
		got := make([]byte, 1)
		if _, err := pr.Read(got); err != nil || got[0] != 'z' {
			t.Fatalf("reading back through the pipe = (%q, %v), want z", got, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// payloadPipe returns the read end of a pipe holding payload, its write end
// closed so a reader meets EOF after it.
func payloadPipe(t *testing.T, payload string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe = %v", err)
	}
	if _, err := w.WriteString(payload); err != nil {
		t.Fatalf("writing %q = %v", payload, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the payload writer = %v", err)
	}
	return r
}

// runSealProbe fstat's the descriptor named in sealProbeFDsEnv and reads fd 3
// and 4, then prints what it found.
func runSealProbe(t *testing.T) {
	t.Helper()
	fd, err := strconv.Atoi(os.Getenv(sealProbeFDsEnv))
	if err != nil {
		t.Fatalf("probe fd = %v", err)
	}
	var st syscall.Stat_t
	state := "closed"
	if syscall.Fstat(fd, &st) == nil {
		state = "open"
	}
	extras := ""
	for _, n := range []uintptr{3, 4} {
		buf := make([]byte, 8)
		k, _ := os.NewFile(n, "extra").Read(buf)
		extras += string(buf[:k])
	}
	if _, err := os.Stdout.WriteString("leaked=" + state + " extras=" + extras + "\n"); err != nil {
		t.Fatalf("writing the report = %v", err)
	}
}

// fdPolicy is the Spec.FDs a row asks for.
func fdPolicy(inherit bool) coreproc.FDPolicy {
	if inherit {
		return coreproc.FDsInherited
	}
	return coreproc.FDsSealed
}
