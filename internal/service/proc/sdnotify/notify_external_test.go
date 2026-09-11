//go:build unix

// Package sdnotify_test — the notifier side as a service uses it.
//
// Every test here binds a real AF_UNIX datagram socket and reads what the
// notifier wrote to it, which is the only way to prove the wire format. Windows
// has no such socket, so the whole file is Unix-only; the no-op contract off
// Linux is covered by sdnotify_other_test.go.
package sdnotify_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// fillTimeout bounds one filler write. It is short because a filler that has
// to wait has already told us what we needed: the queue is full.
const fillTimeout time.Duration = 100 * time.Millisecond

// recvTimeout bounds every wait on a datagram that has already been sent.
const recvTimeout time.Duration = 5 * time.Second

// sunPathMax is the smallest sun_path any platform in the matrix offers — macOS
// and the BSDs stop there, Linux allows 108 — and it is the WHOLE socket path
// that has to fit inside it.
const sunPathMax int = 104

// shortTempDir returns a directory short enough to hold an AF_UNIX socket path.
//
// t.TempDir() embeds the test's name in the directory it makes, and on macOS
// TMPDIR is already /var/folders/<two hashed components>/T — so a table-driven
// subtest name overruns the limit and bind fails with EINVAL, which reads as
// "the socket is wrong" rather than "the path is too long".
func shortTempDir(t *testing.T) string {
	t.Helper()
	//: /tmp is the shortest base every Unix has; TMPDIR is the one that is long.
	dir, err := os.MkdirTemp("/tmp", "ktn")
	if err != nil {
		t.Fatalf("creating a short temporary directory: %v", err)
	}
	t.Cleanup(func() {
		//: best-effort: the socket is unlinked with the directory.
		if rerr := os.RemoveAll(dir); rerr != nil {
			t.Logf("removing %s: %v", dir, rerr)
		}
	})
	return dir
}

// listenOn binds a datagram socket in a temporary directory and returns its
// path plus a function that reads the next datagram.
func listenOn(t *testing.T) (path string, next func() string) {
	t.Helper()
	path = filepath.Join(shortTempDir(t), "n.sock")
	//: fail with the actual reason rather than letting bind report EINVAL,
	//: which says nothing about the length.
	if len(path) >= sunPathMax {
		t.Fatalf("the socket path is %d bytes, over the %d-byte sun_path limit: %s",
			len(path), sunPathMax, path)
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatalf("binding %s: %v", path, err)
	}
	t.Cleanup(func() {
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing the listener: %v", cerr)
		}
	})
	return path, func() string {
		t.Helper()
		if derr := conn.SetReadDeadline(time.Now().Add(recvTimeout)); derr != nil {
			t.Fatalf("setting the read deadline: %v", derr)
		}
		buf := make([]byte, 4096)
		n, rerr := conn.Read(buf)
		if rerr != nil {
			t.Fatalf("reading the datagram: %v", rerr)
		}
		return string(buf[:n])
	}
}

// TestNotify pins the two halves of the libsystemd contract that matter most.
//
// An UNSET socket is a silent no-op returning nil, because a service must run
// identically under systemd and from a shell — reporting an error there would
// make every unsupervised run fail at startup for a reason that is not a fault.
// A SET but broken socket is the opposite: the supervisor is expecting to hear
// from us and did not, so it has to be reported.
func TestNotify(t *testing.T) {
	//: not parallel — every case sets $NOTIFY_SOCKET, which is process-wide.
	type tc struct {
		name string
		//: the socket value to export; "bind" means a real listener.
		socket string
		state  map[string]string
		//: the datagram the listener must receive, when one is bound.
		wantBody string
		wantErr  bool
	}
	tests := []tc{
		{name: "an unset socket is a no-op", socket: "", state: map[string]string{"READY": "1"}},
		{name: "an unset socket with a nil state", socket: ""},
		{
			name:     "a bound socket receives the datagram",
			socket:   "bind",
			state:    map[string]string{"READY": "1"},
			wantBody: "READY=1\n",
		},
		{
			//: an empty state still opens and closes the connection, matching
			//: libsystemd's "ping" behaviour.
			name:     "an empty state still pings",
			socket:   "bind",
			state:    map[string]string{},
			wantBody: "",
		},
		{
			name:    "a socket path nothing is listening on",
			socket:  "/nonexistent/sdk-sdnotify-test.sock",
			state:   map[string]string{"READY": "1"},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var next func() string
		socket := c.socket
		if c.socket == "bind" {
			socket, next = listenOn(t)
		}
		t.Setenv("NOTIFY_SOCKET", socket)

		err := svcsdnotify.Notify(c.state)

		if c.wantErr {
			//: the supervisor was expecting this and did not get it, so the
			//: failure is real and typed.
			if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
				t.Fatalf("Notify = %v, want NOTIFY_FAILED", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Notify = %v, want nil", err)
		}
		if next == nil {
			return
		}
		if got := next(); got != c.wantBody {
			t.Errorf("the supervisor received %q, want %q", got, c.wantBody)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestNotifyRejectsFieldInjection pins the encoding guard. The datagram body is
// an environment block: fields are newline-separated and NAME=value within a
// line. A name or value carrying those delimiters would forge EXTRA fields —
// a STATUS value ending in "\nMAINPID=1" would tell the supervisor to track
// init as this service's main process.
func TestNotifyRejectsFieldInjection(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name  string
		state map[string]string
	}
	tests := []tc{
		{"a newline in a value", map[string]string{"STATUS": "ok\nMAINPID=1"}},
		{"a newline in a name", map[string]string{"STATUS\nMAINPID": "1"}},
		{"an equals sign in a name", map[string]string{"STATUS=x": "1"}},
		{"a leading newline in a value", map[string]string{"STATUS": "\nREADY=1"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, _ := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		err := svcsdnotify.Notify(c.state)

		//: the refusal is a malformed-notification fault, not a send fault:
		//: nothing was wrong with the socket.
		if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
			t.Fatalf("Notify = %v, want INVALID_NOTIFICATION", err)
		}
		//: the forged field must not appear anywhere in an encoding of this
		//: state — the guard runs BEFORE the write, so nothing was sent at all.
		for name, value := range c.state {
			if strings.ContainsAny(name, "\n=") || strings.ContainsRune(value, '\n') {
				continue
			}
			t.Errorf("the case %q carries no injecting field, so it proves nothing", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestReady pins the startup-complete signal.
//
// systemd keys on the NAME: a Ready() that sent STARTED=1 would leave the unit
// waiting for a readiness notification that never arrives, and it would time out
// at startup with the service running perfectly.
func TestReady(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
	}
	tests := []tc{{"a bound supervisor socket"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.Ready(); err != nil {
			t.Fatalf("Ready = %v, want nil", err)
		}

		got := next()
		if got != "READY=1\n" {
			t.Errorf("Ready sent %q, want %q", got, "READY=1\n")
		}
		//: exactly one field — a helper that sent two would tell the supervisor
		//: something it was never asked to.
		if strings.Count(got, "\n") != 1 {
			t.Errorf("Ready sent %d fields, want 1", strings.Count(got, "\n"))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestReloading pins the start of a reload cycle.
//
// A supervisor suppresses health checks between RELOADING and the next READY, so
// a wrong name here means the service is probed mid-reload and restarted for
// failing a check it was never going to pass.
func TestReloading(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
	}
	tests := []tc{{"a bound supervisor socket"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.Reloading(); err != nil {
			t.Fatalf("Reloading = %v, want nil", err)
		}

		got := next()
		if got != "RELOADING=1\n" {
			t.Errorf("Reloading sent %q, want %q", got, "RELOADING=1\n")
		}
		//: exactly one field — a helper that sent two would tell the supervisor
		//: something it was never asked to.
		if strings.Count(got, "\n") != 1 {
			t.Errorf("Reloading sent %d fields, want 1", strings.Count(got, "\n"))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestStopping pins the start of a graceful shutdown.
//
// Without it the supervisor cannot tell a deliberate shutdown from a crash, and
// a restart policy of on-failure would bring the service straight back up.
func TestStopping(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
	}
	tests := []tc{{"a bound supervisor socket"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.Stopping(); err != nil {
			t.Fatalf("Stopping = %v, want nil", err)
		}

		got := next()
		if got != "STOPPING=1\n" {
			t.Errorf("Stopping sent %q, want %q", got, "STOPPING=1\n")
		}
		//: exactly one field — a helper that sent two would tell the supervisor
		//: something it was never asked to.
		if strings.Count(got, "\n") != 1 {
			t.Errorf("Stopping sent %d fields, want 1", strings.Count(got, "\n"))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestWatchdog pins the watchdog keep-alive.
//
// This one is load-bearing in the most literal sense: miss the interval and the
// supervisor kills the process. A wrong field name means every ping is ignored.
func TestWatchdog(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
	}
	tests := []tc{{"a bound supervisor socket"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.Watchdog(); err != nil {
			t.Fatalf("Watchdog = %v, want nil", err)
		}

		got := next()
		if got != "WATCHDOG=1\n" {
			t.Errorf("Watchdog sent %q, want %q", got, "WATCHDOG=1\n")
		}
		//: exactly one field — a helper that sent two would tell the supervisor
		//: something it was never asked to.
		if strings.Count(got, "\n") != 1 {
			t.Errorf("Watchdog sent %d fields, want 1", strings.Count(got, "\n"))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestStatus pins the free-text progress line, which is what an operator sees in
// `systemctl status`. The value is sent verbatim — including an empty one, which
// is how a service clears a stale message rather than leaving the last one up
// forever.
func TestStatus(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
		msg  string
	}
	tests := []tc{
		{"a progress message", "warming up"},
		{"an empty message clears the line", ""},
		{"a message with spaces and punctuation", "serving 3 of 5 shards, ok"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.Status(c.msg); err != nil {
			t.Fatalf("Status = %v, want nil", err)
		}

		want := "STATUS=" + c.msg + "\n"
		if got := next(); got != want {
			t.Errorf("Status sent %q, want %q", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestMainPID pins the pid advertisement, which lets a supervisor track a
// process other than the notifier itself — the shape a service takes when it
// forks a worker and hands supervision over to it.
func TestMainPID(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	type tc struct {
		name string
		pid  int
	}
	tests := []tc{
		{"a plausible pid", 4242},
		{"pid 1", 1},
		{"our own pid", os.Getpid()},
		//: a zero or negative pid is nonsense the supervisor will refuse, but
		//: the encoder's job is to send what it was given, not to judge it.
		{"a zero pid", 0},
		{"a negative pid", -1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket, next := listenOn(t)
		t.Setenv("NOTIFY_SOCKET", socket)

		if err := svcsdnotify.MainPID(c.pid); err != nil {
			t.Fatalf("MainPID = %v, want nil", err)
		}

		want := "MAINPID=" + strconv.Itoa(c.pid) + "\n"
		if got := next(); got != want {
			t.Errorf("MainPID sent %q, want %q", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// fillReceiveQueue leaves path in the state a supervisor that has stopped
// reading produces: a receive queue so full that a BRAND NEW sender blocks on
// its first datagram. That distinction is the whole harness. On Linux the
// blocking is accounted per SENDER while the queue is bounded by the RECEIVER,
// so filling from one connection only blocks that one — measured: a single
// sender stopped at its 13th 8 KiB datagram while a fresh socket still wrote in
// 10 µs. What stops everybody is the receiver's queue LENGTH, reached at 514
// small datagrams on this kernel — and every sdnotify send is a fresh socket
// writing one small datagram, which is exactly the shape that then blocks.
//
// So the fill uses fresh senders too, and stops when one of them blocks.
func fillReceiveQueue(t *testing.T, path string) {
	t.Helper()
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	//: a bounded loop: the queue is finite, and a kernel that never refuses one
	//: of these is one this test cannot describe.
	for range 8192 {
		conn, derr := net.DialUnix("unixgram", nil, addr)
		if derr != nil {
			t.Fatalf("dialling %s: %v", path, derr)
		}
		if derr = conn.SetWriteDeadline(time.Now().Add(fillTimeout)); derr != nil {
			t.Fatalf("setting the filler's write deadline: %v", derr)
		}
		_, werr := conn.Write([]byte("FILL=1\n"))
		//: the queued datagrams outlive their sender, so closing costs nothing.
		if cerr := conn.Close(); cerr != nil {
			t.Logf("closing a filler: %v", cerr)
		}
		if werr != nil {
			//: a fresh sender now blocks — the state we came for.
			return
		}
	}
	t.Fatal("the receive queue never filled, so the test cannot block a sender")
}

// TestNotifyContextStopsWaitingWhenTheSupervisorStopsReading is the whole
// reason the context sibling exists. A unixgram write blocks once the
// RECEIVER's buffer is full, and the receiver is the supervisor: a paused,
// stopped or merely slow systemd leaves this process waiting with nothing it
// can do about it. Notify has no deadline and waits forever; NotifyContext
// stops at the caller's.
//
// Seen failing against Notify: the same send never returned, and the test
// binary was killed by its own timeout — "panic: test timed out after 20s".
func TestNotifyContextStopsWaitingWhenTheSupervisorStopsReading(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET, which is process-wide.
	socket, _ := listenOn(t)
	fillReceiveQueue(t, socket)
	t.Setenv("NOTIFY_SOCKET", socket)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := svcsdnotify.NotifyContext(ctx, map[string]string{"READY": "1"})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("a send into a full buffer reported success")
	}
	//: the datagram did not go out, so it is the domain's own send failure —
	//: and the cause survives, because a caller distinguishing "too slow" from
	//: "no socket" reads it through errors.Is.
	if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
		t.Errorf("err = %v, want NOTIFY_FAILED", err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("err = %v, want it to carry os.ErrDeadlineExceeded", err)
	}
	//: a generous ceiling: what is under test is that it RETURNS, not its
	//: precision. Without the bound it never does.
	if elapsed > 5*time.Second {
		t.Errorf("the send took %v, want it bounded by the 100ms context", elapsed)
	}
}

// TestNotifyContextIsCancellableWithNoDeadlineAtAll covers the other half of
// the bound: a context with no deadline still reaches a write already parked on
// the supervisor's buffer, because a net.Conn has no other way to be
// interrupted and the deadline set in the past is what unparks it.
// # Goroutine lifetime
//
// One goroutine holds the parked send. It ends when the send returns, which
// the cancel below is what causes; the receive after it is what waits for that
// to have happened, so nothing outlives the test.
func TestNotifyContextIsCancellableWithNoDeadlineAtAll(t *testing.T) {
	//: not parallel — it sets $NOTIFY_SOCKET.
	socket, _ := listenOn(t)
	fillReceiveQueue(t, socket)
	t.Setenv("NOTIFY_SOCKET", socket)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		//: the send parks on the full buffer until the cancel below.
		done <- svcsdnotify.NotifyContext(ctx, map[string]string{"READY": "1"})
	}()
	//: cancel from the outside, which is the supervisor-independent exit.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled send reported success")
		}
		if !errs.HasCode(err, coreproc.CodeNotifyFailed) {
			t.Errorf("err = %v, want NOTIFY_FAILED", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled send never returned")
	}
}
