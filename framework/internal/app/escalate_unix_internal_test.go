//go:build unix

package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// escalateRoleEnv tells the test binary, run again as a child, to be the
// product TestASecondSIGTERMEndsTheDrainAtOnce signals.
const escalateRoleEnv string = "KITSUNIUM_APP_ESCALATE_ROLE"

// escalateMarker prefixes the lines the child writes for its parent.
const escalateMarker string = "escalate: "

// TestASecondSIGTERMEndsTheDrainAtOnce pins #1096's framework criterion: a
// second SIGTERM during a drain whose component's stop blocks ends App.Run
// through the zero-grace path, not at its stop deadline. The signals never
// reach the test binary: it runs itself again as a child (-test.run naming
// this test, the role in its environment), the child runs a product whose
// consumer's handler blocks with a drain of an hour, and this test sends both
// SIGTERMs to that child alone. Unix only: Windows has no SIGTERM to send to
// another process.
func TestASecondSIGTERMEndsTheDrainAtOnce(t *testing.T) {
	if os.Getenv(escalateRoleEnv) == "child" {
		os.Exit(escalateChild())
	}
	child := exec.Command(os.Args[0], "-test.run=^TestASecondSIGTERMEndsTheDrainAtOnce$", "-test.count=1")
	child.Env = append(os.Environ(), escalateRoleEnv+"=child")
	child.Stderr = os.Stderr
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			if text, ok := strings.CutPrefix(scanner.Text(), escalateMarker); ok {
				lines <- text
			}
		}
		close(lines)
		exited <- child.Wait()
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-exited
	})
	await := func(want string) string {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("the child ended before %q", want)
				}
				if strings.HasPrefix(line, want) {
					return line
				}
			case <-deadline:
				t.Fatalf("no %q from the child", want)
			}
		}
	}
	await("serving")
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	await("draining")
	second := time.Now()
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	returned := await("returned")
	if took := time.Since(second); took > 10*time.Second {
		t.Fatalf("the run returned %v after the second SIGTERM (%s), want at once", took, returned)
	}
	t.Logf("child: %s, %v after the second SIGTERM", returned, time.Since(second).Round(time.Millisecond))
}

// escalateChild is the child's side: a product whose consumer's handler
// blocks, its drain an hour long, run with App.Run and reporting each step on
// standard output. It returns the exit status.
//
// Goroutine lifecycle: one goroutine runs the app and reports on a buffered
// channel; the child exits when it reports, or after a minute regardless.
func escalateChild() int {
	say := func(format string, args ...any) { fmt.Printf(escalateMarker+format+"\n", args...) }
	b := newBlockingTicks("escalate", Drain(time.Hour))
	a := NewApp(b.svc.name, b.svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	giveUp := time.After(time.Minute)
	for a.URL() == "" {
		select {
		case err := <-done:
			say("returned before serving: %v", err)
			return 2
		case <-giveUp:
			say("stuck before serving")
			return 3
		case <-time.After(time.Millisecond):
		}
	}
	if err := b.ticks.Publish(context.Background(), tick{N: 1}); err != nil {
		say("publish: %v", err)
		return 2
	}
	<-b.started
	say("serving")
	for !escalateStopping(a) {
		select {
		case <-giveUp:
			say("stuck before draining")
			return 3
		case <-time.After(time.Millisecond):
		}
	}
	say("draining")
	select {
	case err := <-done:
		say("returned: %v", err)
		return 0
	case <-giveUp:
		say("stuck draining")
		return 3
	}
}

// escalateStopping reports whether a's stop began.
func escalateStopping(a *App) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopping
}
