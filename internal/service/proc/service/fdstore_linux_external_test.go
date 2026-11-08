//go:build linux

package service_test

import (
	"context"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
	svcnotify "github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// The descriptor store over real processes and a real notify socket (S0d,
// kitsunium/sdk#736): this binary is the service, sending its descriptors
// with SCM_RIGHTS and reading back what it is handed at fd 3 on.

// storeDirEnv names the directory a store helper reports to.
const storeDirEnv = "PROC_SERVICE_TEST_STORE_DIR"

func init() {
	helpers["fdstore"] = storeHelper
	helpers["fdstore-hup"] = hangupHelper
}

// sendFDs sends one notification carrying files over $NOTIFY_SOCKET, as
// sd_pid_notify_with_fds does.
func sendFDs(state string, files ...*os.File) error {
	sock, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(sock) }()
	fds := make([]int, 0, len(files))
	for _, f := range files {
		fds = append(fds, int(f.Fd()))
	}
	return syscall.Sendmsg(sock, []byte(state), syscall.UnixRights(fds...), &syscall.SockaddrUnix{Name: os.Getenv("NOTIFY_SOCKET")}, 0)
}

// pipeHolding returns a pipe's read end holding text, its write end closed.
func pipeHolding(text string) *os.File {
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(3)
	}
	if _, err = w.WriteString(text); err != nil {
		os.Exit(3)
	}
	_ = w.Close()
	return r
}

// awaitTerm waits for SIGTERM, then exits 0.
func awaitTerm() {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	<-term
	os.Exit(0)
}

// fileHolding returns an unlinked file of dir holding text: no hangup ever
// removes it from a store.
func fileHolding(dir, text string) *os.File {
	f, err := os.CreateTemp(dir, "state")
	if err != nil {
		os.Exit(3)
	}
	if _, err = f.WriteString(text); err != nil || os.Remove(f.Name()) != nil {
		os.Exit(3)
	}
	return f
}

// listening returns a listening Unix socket bound in dir: watched for a
// hangup, it never hangs up.
func listening(dir, name string) *os.File {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, name), Net: "unix"})
	if err != nil {
		os.Exit(3)
	}
	f, err := l.File()
	if err != nil {
		os.Exit(3)
	}
	return f
}

// storeHelper is a service with state to keep. Its first run stores a file
// holding "state-1" as "state", then two listening sockets as "extra" — one
// past FileDescriptorStoreMax=2. A later run reports what it was handed,
// stores "state" again — a duplicate —, removes "extra", and is ready.
func storeHelper() {
	dir := os.Getenv(storeDirEnv)
	n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if n == 0 {
		if sendFDs("FDSTORE=1\nFDNAME=state", fileHolding(dir, "state-1")) != nil ||
			sendFDs("FDSTORE=1\nFDNAME=extra", listening(dir, "a"), listening(dir, "b")) != nil {
			os.Exit(2)
		}
	} else {
		names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
		var report strings.Builder
		report.WriteString(os.Getenv("LISTEN_FDS") + "|" + os.Getenv("LISTEN_FDNAMES") + "|")
		report.WriteString(strconv.FormatBool(os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid())) + "|")
		for i := range n {
			f := os.NewFile(uintptr(3+i), names[i])
			if names[i] == "state" {
				body := make([]byte, 16)
				k, _ := f.ReadAt(body, 0)
				report.Write(body[:k])
				if sendFDs("FDSTORE=1\nFDNAME=state", f) != nil {
					os.Exit(2)
				}
			}
		}
		if sendFDs("FDSTOREREMOVE=1\nFDNAME=extra") != nil {
			os.Exit(2)
		}
		if os.WriteFile(filepath.Join(dir, "handed"), []byte(report.String()), 0o600) != nil {
			os.Exit(2)
		}
	}
	if svcnotify.Notify(map[string]string{"READY": "1"}) != nil {
		os.Exit(2)
	}
	awaitTerm()
}

// hangupHelper stores two pipes whose write ends it closed — "hup" watched,
// "keep" with FDPOLL=0 — and is ready.
func hangupHelper() {
	if sendFDs("FDSTORE=1\nFDNAME=hup", pipeHolding("")) != nil ||
		sendFDs("FDSTORE=1\nFDNAME=keep\nFDPOLL=0", pipeHolding("")) != nil ||
		svcnotify.Notify(map[string]string{"READY": "1"}) != nil {
		os.Exit(2)
	}
	awaitTerm()
}

// storeUnit builds a notify unit over ListenNotify running this binary as
// helper, its store of max descriptors, restarted at once after a crash; its
// ExecStartPre= reports the descriptors a command holds.
func storeUnit(t *testing.T, helper, dir string, max int) *service.Unit {
	t.Helper()
	t.Cleanup(func() { killHolders(t, dir) })
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	scan := `fds=""; for n in $(seq 0 64); do [ -e /proc/self/fd/$n ] && fds="$fds $n"; done; echo "$fds" >> "$0"`
	cfg := service.Config{
		Name: "store", Type: coresvc.TypeNotify, Notify: src, FileDescriptorStoreMax: max,
		Restart: coresvc.RestartAlways, RestartDelay: 10 * time.Millisecond,
		StartLimitBurst: restartBurst, StartLimitInterval: restartWindow,
		Spec:         coreproc.Spec{Env: helperEnviron(helper, storeDirEnv+"="+dir)},
		ExecStartPre: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", scan, filepath.Join(dir, "pre"))},
		ExecStart:    []coresvc.CommandValue{command(t, os.Args[0], "-test.run=^$")},
	}
	u, err := service.NewUnit(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = u.Close(ctx)
	})
	return u
}

// crash kills the unit's main process — this test's own child — and waits
// for the restarted one to be up.
func crash(t *testing.T, u *service.Unit, sub <-chan coresvc.EventValue) {
	t.Helper()
	main := u.State().MainPID
	if err := syscall.Kill(main, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", main, err)
	}
	waitState(t, sub, coresvc.StateAutoRestart)
	waitState(t, sub, coresvc.StateActive)
}

// storeCycle runs one unit through a start, a crash and its restart, then
// closes it, and returns what the restarted process reported.
func storeCycle(t *testing.T) (handed string, listed []coresvc.StoredFDValue, refused uint64, pre string) {
	t.Helper()
	dir := t.TempDir()
	u := storeUnit(t, "fdstore", dir, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sub, _ := u.Subscribe(256)
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	crash(t, u, sub)
	raw, err := os.ReadFile(filepath.Join(dir, "handed"))
	if err != nil {
		t.Fatalf("the restarted service reported nothing: %v", err)
	}
	// FDSTOREREMOVE=1 follows the report: wait until the store took it.
	deadline := time.Now().Add(10 * time.Second)
	for len(u.FDStore()) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	listed, refused = u.FDStore(), u.State().FDStoreRefused
	if err = u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if left := u.FDStore(); len(left) != 0 {
		t.Errorf("a stopped unit (FileDescriptorStorePreserve=restart) keeps %v", left)
	}
	if err = u.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	preRaw, _ := os.ReadFile(filepath.Join(dir, "pre"))
	return string(raw), listed, refused, string(preRaw)
}

// TestRealFDStore: what the service stored is handed to its restarted main
// process at fd 3 on — its names in LISTEN_FDNAMES, newest first, LISTEN_PID
// its own, the pipe still holding what the first run wrote —, never to
// ExecStartPre=; the descriptor past FileDescriptorStoreMax= was refused, the
// duplicate dropped, FDSTOREREMOVE=1 obeyed, and the stop released the rest.
func TestRealFDStore(t *testing.T) {
	handed, listed, refused, pre := storeCycle(t)
	if handed != "2|extra:state|true|state-1" {
		t.Errorf("the restarted service was handed %q, want 2|extra:state|true|state-1", handed)
	}
	if len(listed) != 1 || listed[0].Name != "state" || !listed[0].Mode.IsRegular() || listed[0].Poll || listed[0].Inode == 0 {
		t.Errorf("FDStore = %+v, want the state file alone, unwatched (epoll cannot)", listed)
	}
	if refused != 1 {
		t.Errorf("FDStoreRefused = %d, want 1", refused)
	}
	for _, line := range strings.Split(strings.TrimSpace(pre), "\n") {
		if strings.TrimSpace(line) != "0 1 2" {
			t.Errorf("ExecStartPre= held fds %q, want 0 1 2 alone", line)
		}
	}
}

// TestRealFDStoreLeaksNothing: ten units through a start, a crash, a
// restart, a stop and a close leave this process's descriptor table as they
// found it — every descriptor stored, refused, duplicated, removed or
// released was closed, the notify socket and the hangup watch too.
func TestRealFDStoreLeaksNothing(t *testing.T) {
	storeCycle(t) // the runtime's and the spawner's own descriptors, opened once
	before := settledDescriptors(t)
	for range 10 {
		storeCycle(t)
	}
	sameDescriptors(t, "ten units", before, descriptors(t))
}

// TestRealFDStoreHangup: a stored pipe whose writer is gone leaves the store
// on its own; one stored with FDPOLL=0 stays.
func TestRealFDStoreHangup(t *testing.T) {
	u := storeUnit(t, "fdstore-hup", t.TempDir(), 4)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = u.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := u.FDStore(); len(got) == 1 && got[0].Name == "keep" && !got[0].Poll {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("FDStore = %+v, want keep alone (unwatched) once hup hung up", u.FDStore())
}
