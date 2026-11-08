//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// Type=forking over real daemons (S0c, kitsunium/sdk#735): /bin/sh forks
// them the way a traditional daemon does. Every process a test starts carries
// a random mark in its environment; the test kills what carries its mark, and
// nothing else, when it ends.

// markOf returns a fresh mark — an environment assignment — and registers the
// cleanup that kills every process still carrying it.
func markOf(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	mark := "PROC_SERVICE_TEST_MARK=" + hex.EncodeToString(raw)
	t.Cleanup(func() {
		for _, pid := range marked(mark) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return mark
}

// marked lists the live processes whose environment holds mark: this test's
// own descendants, the only ones it may signal.
func marked(mark string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil || pid == os.Getpid() {
			continue
		}
		env, readErr := os.ReadFile("/proc/" + e.Name() + "/environ")
		if readErr != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if string(kv) == mark {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// markCgroup stands for a unit's cgroup: its members are the processes that
// carry the mark. It lists them (CgroupMembers) and signals nothing.
type markCgroup struct{ mark string }

func (markCgroup) Kill() error                  { return nil }
func (markCgroup) Signal(coreproc.Signal) error { return nil }

func (c markCgroup) Contains(pid int) (bool, error) {
	for _, p := range marked(c.mark) {
		if p == pid {
			return true, nil
		}
	}
	return false, nil
}

func (c markCgroup) Members() ([]int, error) { return marked(c.mark), nil }

// readPID reads a PID a script wrote, failing the test when there is none.
func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no PID at %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("PID file %s holds %q", path, raw)
	}
	return pid
}

// daemonize is a forking command: it double-forks a daemon running body in a
// session of its own, waits for the files the daemon writes, then exits 0.
// The ":" prefix keeps the engine from expanding the script's $.
func daemonize(t *testing.T, body string, wait ...string) coresvc.CommandValue {
	t.Helper()
	var waits strings.Builder
	for _, path := range wait {
		waits.WriteString(`while [ ! -s '` + path + `' ]; do sleep 0.01; done; `)
	}
	script := `( setsid sh -c '` + body + `' & ); ` + waits.String() + `exit 0`
	return command(t, ":/bin/sh", "-c", script)
}

// forkingUnit builds a Type=forking unit over proc/exec, NewAttacher bounded
// to this test's user, and the mark in every process's environment.
func forkingUnit(t *testing.T, mark string, cfg service.Config) *service.Unit {
	t.Helper()
	cfg.Type = coresvc.TypeForking
	cfg.Attacher = service.NewAttacher(coreproc.AttachValue{UIDs: []int{os.Getuid()}})
	cfg.Spec = coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin", mark}}
	return realUnit(t, cfg)
}

// TestRealForkingPIDFile: a daemon that double-forks and writes its PID file
// is adopted — the unit active, its main process the file's PID —, and its
// death is seen: the unit inactive, its end clean (no status is knowable).
func TestRealForkingPIDFile(t *testing.T) {
	mark := markOf(t)
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	u := forkingUnit(t, mark, service.Config{
		PIDFile:   pidFile,
		ExecStart: []coresvc.CommandValue{daemonize(t, `echo $$ > "`+pidFile+`"; exec sleep 60`, pidFile)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sub, _ := u.Subscribe(64)
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	daemon := readPID(t, pidFile)
	if st := u.State(); st.State != coresvc.StateActive || st.MainPID != daemon {
		t.Fatalf("after Start: %s, main %d; the daemon is %d", st.State, st.MainPID, daemon)
	}
	if err := syscall.Kill(daemon, syscall.SIGKILL); err != nil {
		t.Fatalf("killing the daemon: %v", err)
	}
	ev := waitState(t, sub, coresvc.StateInactive)
	if ev.Result != coresvc.ResultSuccess || ev.MainPID != 0 {
		t.Fatalf("the daemon gone: result %s, main %d", ev.Result, ev.MainPID)
	}
}

// TestRealForkingPIDFileRefused: a PID file that is absent, a symbolic link
// or garbage fails the start once TimeoutStartSec= passed — START_FAILED with
// the result timeout, naming PID_FILE_REFUSED — and never earlier.
func TestRealForkingPIDFileRefused(t *testing.T) {
	const timeout = 400 * time.Millisecond
	for _, tc := range []struct {
		name    string
		prepare func(path string) error
	}{
		{"absent", func(string) error { return nil }},
		{"symlink", func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
		{"garbage", func(path string) error { return os.WriteFile(path, []byte("not a pid\n"), 0o600) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mark := markOf(t)
			pidFile := filepath.Join(t.TempDir(), "daemon.pid")
			if err := tc.prepare(pidFile); err != nil {
				t.Fatal(err)
			}
			u := forkingUnit(t, mark, service.Config{
				PIDFile: pidFile, TimeoutStart: timeout,
				ExecStart: []coresvc.CommandValue{command(t, "/bin/sh", "-c", "exit 0")},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			begin := time.Now()
			err := u.Start(ctx)
			if !errs.HasCode(err, coresvc.CodeStartFailed) {
				t.Fatalf("Start: %v, want START_FAILED", err)
			}
			if elapsed := time.Since(begin); elapsed < timeout {
				t.Errorf("failed after %s, before TimeoutStartSec=%s", elapsed, timeout)
			}
			if st := u.State(); st.State != coresvc.StateFailed || st.Result != coresvc.ResultTimeout {
				t.Errorf("after the start: %s %s, want failed timeout", st.State, st.Result)
			}
			if e, ok := errors.AsType[*errs.Error](err); !ok || !hasField(e, "pidfile", "PID_FILE_REFUSED") {
				t.Errorf("START_FAILED does not name the PID file's refusal: %v", err)
			}
		})
	}
}

// hasField reports whether an SDK error carries key=value.
func hasField(e *errs.Error, key, value string) bool {
	for _, f := range e.Fields() {
		if f.Key() == key && f.StringValue() == value {
			return true
		}
	}
	return false
}

// TestRealForkingGuess: without PIDFile=, a daemon with two workers is
// guessed — the one member whose parent is outside the cgroup —; two daemons
// leave the main process unknown, and so does GuessMainPID=no: the unit is up
// while its cgroup has members.
func TestRealForkingGuess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		daemons int
		workers bool
		guess   coresvc.Toggle
		known   bool
	}{
		{"one daemon and its workers", 1, true, coresvc.ToggleDefault, true},
		{"two daemons", 2, false, coresvc.ToggleYes, false},
		{"GuessMainPID=no", 1, false, coresvc.ToggleNo, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mark := markOf(t)
			dir := t.TempDir()
			var script strings.Builder
			var files []string
			for i := range tc.daemons {
				file := filepath.Join(dir, "d"+strconv.Itoa(i)+".pid")
				files = append(files, file)
				body := `echo $$ > "` + file + `"; exec sleep 60`
				if tc.workers {
					body = `sleep 60 & sleep 60 & echo $$ > "` + file + `"; wait`
				}
				script.WriteString(`( setsid sh -c '` + body + `' & ); `)
			}
			for _, file := range files {
				script.WriteString(`while [ ! -s '` + file + `' ]; do sleep 0.01; done; `)
			}
			script.WriteString(`exit 0`)
			u := forkingUnit(t, mark, service.Config{
				Cgroup: markCgroup{mark: mark}, GuessMainPID: tc.guess,
				ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", script.String())},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := u.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			st := u.State()
			want := 0
			if tc.known {
				want = readPID(t, files[0])
			}
			if st.State != coresvc.StateActive || st.MainPID != want {
				t.Fatalf("after Start: %s, main %d, want active with main %d", st.State, st.MainPID, want)
			}
		})
	}
}
