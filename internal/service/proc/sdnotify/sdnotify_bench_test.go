// Package sdnotify — the measurement answers one question: when a service calls
// Ready() or Watchdog() on a timer, what is it paying for? The datagram body is
// pure string work with a security check in it (a name or value carrying the
// field delimiters is refused, not escaped), and the send is a dial + write +
// close against an AF_UNIX socket. Those two halves are separated here, because
// only one of them is the SDK's to make faster.
//
// It is an INTERNAL benchmark: encodePayload, parsePayload, resolveAddr and
// watchdogInterval are unexported, and they are precisely the pure-CPU half.
package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// benchDrainBuf sizes the socket drain's receive buffer, comfortably above the
// largest datagram any row sends (a 4 KiB STATUS).
const benchDrainBuf int = 8192

// Sinks defeat dead-code elimination on the value-returning helpers.
var (
	errSink    error
	strSink    string
	boolSink   bool
	notifySink coreproc.NotificationValue
	durSink    int64
)

// benchReady is the smallest real datagram: the one every supervised service
// sends exactly once at startup.
var benchReady = map[string]string{"READY": "1"}

// benchStatus is the shape a service publishing progress sends repeatedly.
var benchStatus = map[string]string{"STATUS": "serving 1024 connections, 3 workers idle"}

// benchSix is a fully populated notification — the worst case the protocol
// defines, so the per-field slope is visible against benchReady. It is folded
// from a slice rather than written as a map literal because the protocol's key
// space is a fixed set of strings and a six-key string-map literal is exactly
// what KTN-VAR-STRMAP flags; the map itself is what encodePayload takes.
var benchSix = foldState([][2]string{
	{"READY", "1"},
	{"STATUS", "up"},
	{"MAINPID", "4242"},
	{"WATCHDOG", "1"},
	{"RELOADING", "1"},
	{"STOPPING", "1"},
})

// foldState turns ordered NAME/value pairs into the state map Notify accepts.
func foldState(pairs [][2]string) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		out[kv[0]] = kv[1]
	}
	return out
}

// ── the pure half: formatting and the injection check ────────────────────────

func BenchmarkEncodePayload_Ready(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := encodePayload(benchReady)
		strSink, errSink = s, err
	}
}

func BenchmarkEncodePayload_Status(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := encodePayload(benchStatus)
		strSink, errSink = s, err
	}
}

func BenchmarkEncodePayload_SixFields(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := encodePayload(benchSix)
		strSink, errSink = s, err
	}
}

// BenchmarkEncodePayload_LongStatus prices the injection scan on a value long
// enough for the ContainsRune walk to dominate: STATUS is free-text, so it is
// the one field a caller can make arbitrarily large.
func BenchmarkEncodePayload_LongStatus(b *testing.B) {
	state := map[string]string{"STATUS": strings.Repeat("x", 4096)}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := encodePayload(state)
		strSink, errSink = s, err
	}
}

// BenchmarkEncodePayload_Rejected is the refusal path the injection test pins: a
// value carrying the field delimiter is refused rather than encoded. It is
// measured because a security check that is only cheap when it passes is a check
// an attacker chooses the cost of.
func BenchmarkEncodePayload_Rejected(b *testing.B) {
	state := map[string]string{"STATUS": "ok\nMAINPID=1"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s, err := encodePayload(state)
		strSink, errSink = s, err
	}
}

// ── the listener half: parsing a received datagram ───────────────────────────

func BenchmarkParsePayload_Ready(b *testing.B) {
	body := "READY=1\n"
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v, err := parsePayload(body)
		notifySink, errSink = v, err
	}
}

func BenchmarkParsePayload_SixFields(b *testing.B) {
	body := "READY=1\nSTATUS=up\nMAINPID=4242\nWATCHDOG=1\nRELOADING=1\nSTOPPING=1\n"
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v, err := parsePayload(body)
		notifySink, errSink = v, err
	}
}

// ── the environment queries ──────────────────────────────────────────────────

func BenchmarkResolveAddr_Pathname(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = resolveAddr("/run/systemd/notify")
	}
}

func BenchmarkResolveAddr_Abstract(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = resolveAddr("@/org/freedesktop/systemd1/notify")
	}
}

func BenchmarkWatchdogInterval_Set(b *testing.B) {
	b.Setenv(envWatchdogUsec, "30000000")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d, ok := WatchdogInterval()
		durSink, boolSink = int64(d), ok
	}
}

func BenchmarkWatchdogInterval_Unset(b *testing.B) {
	b.Setenv(envWatchdogUsec, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d, ok := WatchdogInterval()
		durSink, boolSink = int64(d), ok
	}
}

// ── the syscall half: the whole notifier round trip ──────────────────────────

// BenchmarkNotify_Unset is what an UNSUPERVISED binary pays for every Ready() /
// Watchdog() call it makes: one environment lookup and a nil return. libsystemd
// specifies this as a silent no-op, and a service that ships the calls
// unconditionally needs to know they cost nothing when nobody is listening.
func BenchmarkNotify_Unset(b *testing.B) {
	b.Setenv(envNotifySocket, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = Ready()
	}
}

// BenchmarkNotify_Ready is the full send against a real socket this benchmark
// binds and drains: DialUnix, Write, Close. Notify opens a fresh connection per
// call — the libsystemd behaviour — so this row is the price of that decision,
// and the gap against BenchmarkEncodePayload_Ready is how little of it is the
// SDK's formatting.
func BenchmarkNotify_Ready(b *testing.B) {
	benchNotifySocket(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = Ready()
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("Ready: %v", errSink)
	}
}

// BenchmarkNotify_MainPID adds one strconv.Itoa to the same round trip, which is
// how the report shows that the formatting is not where the time goes.
func BenchmarkNotify_MainPID(b *testing.B) {
	benchNotifySocket(b)
	pid := os.Getpid()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = MainPID(pid)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("MainPID: %v", errSink)
	}
}

// BenchmarkNotify_LongStatus sends the 4 KiB free-text status, so the injection
// scan and the copy are both on the wire path rather than measured alone.
func BenchmarkNotify_LongStatus(b *testing.B) {
	benchNotifySocket(b)
	msg := strings.Repeat("x", 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = Status(msg)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("Status: %v", errSink)
	}
}

// benchNotifySocket binds a real unixgram socket in a private directory, points
// $NOTIFY_SOCKET at it, and drains it on a goroutine so a full receive buffer
// never turns the benchmark into a measurement of backpressure. It skips where
// AF_UNIX datagrams are unavailable rather than reporting the cost of a failure.
func benchNotifySocket(b *testing.B) {
	b.Helper()
	// TMPDIR here carries a POSIX ACL, so b.TempDir() would not be private and a
	// mode-checking peer could refuse it. Making the directory by hand and then
	// chmod'ing it (Mkdir's mode is masked by umask) is the portable fix.
	dir := filepath.Join(os.TempDir(), "kitsu-sdnotify-bench-"+strconv.Itoa(os.Getpid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		b.Skipf("cannot create a private socket directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		b.Skipf("cannot narrow the socket directory: %v", err)
	}
	b.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			b.Logf("RemoveAll: %v", err)
		}
	})

	sock := filepath.Join(dir, "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		b.Skipf("AF_UNIX datagram sockets are unavailable here: %v", err)
	}
	done := make(chan struct{})
	// The drain runs inline rather than in a named helper: a helper would take
	// the *net.UnixConn as a parameter, which KTN-API-MINIF asks to be narrowed
	// to a one-method interface — an indirection with no reader and no caller.
	go func() {
		buf := make([]byte, benchDrainBuf)
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, _, rErr := conn.ReadFromUnix(buf); rErr != nil {
				return
			}
		}
	}()
	b.Cleanup(func() {
		close(done)
		if cErr := conn.Close(); cErr != nil {
			b.Logf("conn.Close: %v", cErr)
		}
	})
	b.Setenv(envNotifySocket, sock)
}
