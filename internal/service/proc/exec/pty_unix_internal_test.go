//go:build linux || darwin || freebsd

package exec

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// readUntil drains the terminal's master into a buffer until it holds want or
// the deadline passes, and returns everything read.
func readUntil(t *testing.T, out *syncBuffer, want string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal printed %q, never %q within %v", out.String(), want, within)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return out.String()
}

// startOnPTY starts script on a pseudo-terminal of rows × cols and copies what
// the master reads into the returned buffer.
func startOnPTY(t *testing.T, script string, size *coreproc.PTYValue) (coreproc.Process, coreproc.Terminal, *syncBuffer) {
	t.Helper()
	p, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", script}, Stdio: coreproc.StdioPTY, PTY: size})
	if err != nil {
		t.Fatalf("Start(StdioPTY) = %v", err)
	}
	term, ok := p.(coreproc.Terminal)
	if !ok || term.TTY() == nil {
		t.Fatalf("%T gives no terminal", p)
	}
	out := &syncBuffer{}
	master := term.TTY()
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := master.Read(buf)
			_, _ = out.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = p.Stop(ctx, 0, coreproc.Signal(syscall.SIGKILL))
		_, _ = p.Wait()
		_ = master.Close()
	})
	return p, term, out
}

// Test_Start_onAPseudoTerminal pins #934: the child sees a terminal on fd 0,
// `stty size` prints the Spec's rows and columns, input written to the master
// reaches it, and Size reads the window back.
func Test_Start_onAPseudoTerminal(t *testing.T) {
	t.Parallel()
	p, term, out := startOnPTY(t, "/bin/stty size; test -t 0 && echo is-a-tty; read x; echo got:$x", &coreproc.PTYValue{Rows: 30, Cols: 100})
	readUntil(t, out, "30 100", 5*time.Second)
	readUntil(t, out, "is-a-tty", 5*time.Second)
	size, err := term.Size()
	if err != nil || size != (coreproc.PTYValue{Rows: 30, Cols: 100}) {
		t.Errorf("Size() = (%+v, %v), want 30×100", size, err)
	}
	if _, err := term.TTY().Write([]byte("hello\n")); err != nil {
		t.Fatalf("writing to the master = %v", err)
	}
	readUntil(t, out, "got:hello", 5*time.Second)
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait = %v", err)
	}
}

// Test_Terminal_Resize pins the second row: Resize(50, 120) reaches the child
// as SIGWINCH, after which `stty size` prints the new size.
func Test_Terminal_Resize(t *testing.T) {
	t.Parallel()
	_, term, out := startOnPTY(t, "trap '/bin/stty size' WINCH; echo ready; while :; do /bin/sleep 0.05; done", nil)
	readUntil(t, out, "ready", 5*time.Second)
	start := time.Now()
	if err := term.Resize(50, 120); err != nil {
		t.Fatalf("Resize = %v", err)
	}
	readUntil(t, out, "50 120", 5*time.Second)
	t.Logf("the child printed the new size %v after Resize", time.Since(start))
}
