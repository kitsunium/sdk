//go:build linux

package ipc_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/ipc"
)

// The helpers of TestAnAllowedAccountExchangesALineAndAStrangerIsRefused:
// this test binary, or a copy of it (helperBinary), started as another account
// plays the daemon ("listen") or a client ("dial"). A helper starts no process, and the guard
// below refuses to play a role in any process a helper started: the role
// variable is taken out of a child's environment before it is set, and the
// depth variable a helper is given is one, never more.
const (
	// roleEnv names the helper a test binary plays.
	roleEnv = "IPC_TEST_ROLE"
	// depthEnv is set to "1" in a helper's environment and nowhere else.
	depthEnv = "IPC_TEST_DEPTH"
	// pathEnv, uidsEnv and gidsEnv are the helper's Config.
	pathEnv = "IPC_TEST_PATH"
	uidsEnv = "IPC_TEST_ALLOW_UIDS"
	gidsEnv = "IPC_TEST_ALLOW_GIDS"
	// helperRefusal is the exit status of a process the guard refused.
	helperRefusal = 97
	// helperLifetime bounds a helper's life, whatever became of the test.
	helperLifetime = 60 * time.Second
	// helperWait bounds every wait on a helper's word.
	helperWait = 20 * time.Second
)

// The accounts of the cross-account rows: none needs to exist in the
// password database, root gives a child any uid and gid.
const (
	daemonUID   = 61000
	memberUID   = 61001
	strangerUID = 61002
	oncallGID   = 61100
)

func TestMain(m *testing.M) {
	role, depth := os.Getenv(roleEnv), os.Getenv(depthEnv)
	switch {
	case role == "" && depth == "":
		os.Exit(m.Run())
	case role == "" || depth != "1":
		//: a process a helper started, or a helper at the wrong depth: it
		//: plays nothing and runs no test, so no chain of helpers can grow.
		fmt.Fprintf(os.Stderr, "ipc helper guard: role %q at depth %q refused\n", role, depth)
		os.Exit(helperRefusal)
	}
	//: whatever this helper might start sees depth 2 and is refused.
	if os.Setenv(depthEnv, "2") != nil || os.Unsetenv(roleEnv) != nil {
		os.Exit(helperRefusal)
	}
	time.AfterFunc(helperLifetime, func() { os.Exit(3) })
	switch role {
	case "listen":
		os.Exit(helperListen())
	case "dial":
		os.Exit(helperDial())
	}
	os.Exit(helperRefusal)
}

// helperConfig is the Config the parent wrote into the environment.
func helperConfig() ipc.Config {
	ints := func(raw string) []int {
		var out []int
		for f := range strings.SplitSeq(raw, ",") {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	return ipc.Config{Path: os.Getenv(pathEnv), AllowUIDs: ints(os.Getenv(uidsEnv)), AllowGIDs: ints(os.Getenv(gidsEnv))}
}

// helperListen is the daemon: it listens, says "ready", echoes one line per
// admitted connection, says "refused N" whenever the count moves, and ends
// when its standard input closes — the parent's way to stop it.
func helperListen() int {
	cfg := helperConfig()
	ln, err := ipc.NewListener(&cfg)
	if err != nil {
		fmt.Printf("error %v\n", err)
		return 0
	}
	fmt.Println("ready")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			_, _ = io.WriteString(c, "echo:"+line)
			_ = c.Close()
		}
	}()
	stdin := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdin)
	}()
	var said int64
	for {
		select {
		case <-stdin:
			_ = ln.Close()
			return 0
		case <-time.After(5 * time.Millisecond):
		}
		if n := ln.Refused(); n != said {
			said = n
			fmt.Printf("refused %d\n", n)
		}
	}
}

// helperDial is a client: it dials, sends one line and prints what came back
// — "ok", "eof" (the listener closed it unanswered) or the dial's error.
func helperDial() int {
	cfg := helperConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, &cfg)
	if err != nil {
		fmt.Printf("error %v\n", err)
		return 0
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "hello\n"); err != nil {
		fmt.Printf("eof %v\n", err)
		return 0
	}
	switch line, err := bufio.NewReader(c).ReadString('\n'); {
	case err == nil && line == "echo:hello\n":
		fmt.Println("ok")
	default:
		fmt.Printf("eof %q %v\n", line, err)
	}
	return 0
}

// account is who a helper runs as.
type account struct {
	uid, gid int
	groups   []int
}

// helperEnviron is this environment without the helpers' variables and the
// coverage directory (a helper of another account cannot write it), then the
// role, the depth and extra.
func helperEnviron(role string, extra ...string) []string {
	drop := []string{roleEnv, depthEnv, pathEnv, uidsEnv, gidsEnv, "GOCOVERDIR"}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(drop, name)
	})
	return append(append(env, roleEnv+"="+role, depthEnv+"=1"), extra...)
}

// joinInts renders a list as the helpers read it.
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// helperCommand is binary started as who, playing role on path, killed when
// ctx ends.
func helperCommand(ctx context.Context, binary, dir, role string, who account, path string, uids, gids []int) *exec.Cmd {
	groups := make([]uint32, len(who.groups))
	for i, g := range who.groups {
		groups[i] = uint32(g)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^$")
	cmd.Dir = dir
	cmd.Env = helperEnviron(role, pathEnv+"="+path, uidsEnv+"="+joinInts(uids), gidsEnv+"="+joinInts(gids))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(who.uid), Gid: uint32(who.gid), Groups: groups},
		Pdeathsig:  syscall.SIGKILL,
	}
	return cmd
}

// daemon is a running "listen" helper and the lines it says.
type daemon struct {
	cmd   *exec.Cmd
	stdin io.Closer
	lines chan string
}

// startDaemon starts a "listen" helper and waits for its "ready"; it is
// stopped — its standard input closed, then waited for — when the test ends.
func startDaemon(t *testing.T, binary, dir, path string, uids, gids []int) *daemon {
	t.Helper()
	cmd := helperCommand(context.Background(), binary, dir, "listen", account{daemonUID, daemonUID, []int{oncallGID}}, path, uids, gids)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the daemon as uid %d: %v", daemonUID, err)
	}
	d := &daemon{cmd: cmd, stdin: stdin, lines: make(chan string, 16)}
	go func() {
		defer close(d.lines)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			d.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { d.stop(t) })
	if line := d.next(t); line != "ready" {
		t.Fatalf("the daemon said %q, want ready", line)
	}
	return d
}

// next is the daemon's next line.
func (d *daemon) next(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-d.lines:
		if !ok {
			t.Fatal("the daemon ended")
		}
		return line
	case <-time.After(helperWait):
		t.Fatal("the daemon said nothing in time")
	}
	return ""
}

// stop closes the daemon's standard input and waits for it, killing it — our
// own child — if it does not end in time.
func (d *daemon) stop(t *testing.T) {
	_ = d.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- d.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(helperWait):
		_ = d.cmd.Process.Kill()
		<-done
		t.Log("the daemon had to be killed")
	}
}

// dialAs runs a "dial" helper as who and returns what it printed.
func dialAs(t *testing.T, binary, dir, path string, who account) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), helperWait)
	defer cancel()
	out, err := helperCommand(ctx, binary, dir, "dial", who, path, []int{daemonUID}, nil).Output()
	if err != nil {
		t.Fatalf("the client as uid %d: %v (%s)", who.uid, err, out)
	}
	return strings.TrimSpace(string(out))
}

// helperBinary is the test binary the other accounts run. The binary itself
// when every account can reach and execute it — the linux-init lane's, under
// its workspace —, since a copy may land on a file system that runs nothing:
// that lane's /tmp is docker's --tmpfs, mounted noexec, and an execve there is
// EACCES for root and for 61000 alike. A binary only its owner reaches — go
// test's build directory is 0700 — is copied into dir instead, its mode set
// past the umask, and the test skips where dir's file system is noexec.
// Nothing is written outside dir, nor any mode changed outside it.
func helperBinary(t *testing.T, dir string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("this test binary cannot be found: %v", err)
	}
	//: reachable as it is: run where it lies.
	if everyoneRuns(self) {
		return self
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Skipf("this test binary cannot be read: %v", err)
	}
	binary := filepath.Join(dir, "ipc.test")
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	//: WriteFile's mode passes through the umask.
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatal(err)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(dir, &fs); err == nil && fs.Flags&stNoExec != 0 {
		t.Skipf("%s is on a noexec file system and this test binary %s is not every account's to run", dir, self)
	}
	return binary
}

// stNoExec is statfs(2)'s ST_NOEXEC flag, which package syscall does not
// name on Linux.
const stNoExec = 0x8

// everyoneRuns reports whether every account may execute path: the file is
// executable by others, every directory above it searchable by others, and
// its file system not mounted noexec.
func everyoneRuns(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o001 == 0 {
		return false
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil || fs.Flags&stNoExec != 0 {
		return false
	}
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		info, err := os.Stat(d)
		if err != nil || info.Mode().Perm()&0o001 == 0 {
			return false
		}
		if d == filepath.Dir(d) {
			return true
		}
	}
}

// openToSearch lets the other accounts walk to the test's directory: it gives
// that directory, and the per-test directory the testing package made for it,
// read and search for everyone, then skips unless every directory above lets
// another account do the same. Search alone is not enough: the SDK walks the
// path above a socket's directory with pathchain, which enters each directory
// through os.OpenRoot — open(2) of a directory, which needs read permission —,
// so a 0711 component fails the listener's and the dialler's walk alike
// ("the path to it cannot be resolved", DIRECTORY_UNSAFE). Nothing above the
// two directories is changed, and nothing in them is opened wider than the
// row needs: the binary 0755, the socket's directory 0750.
func openToSearch(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for d := filepath.Dir(filepath.Dir(dir)); ; d = filepath.Dir(d) {
		info, err := os.Stat(d)
		if err != nil || info.Mode().Perm()&0o005 != 0o005 {
			t.Skipf("%s does not let another account read and search it; the rows need a TMPDIR every account can walk", d)
		}
		if d == filepath.Dir(d) {
			return
		}
	}
}

// TestAnAllowedAccountExchangesALineAndAStrangerIsRefused is #1092's
// end-to-end row with real accounts, which only root can make: the daemon
// runs as uid 61000 with the on-call group 61100 as a supplementary group, in
// a 0750 directory 61000:61100, its listener with AllowGIDs [61100]. A member
// of the group (uid 61001, the group supplementary, never its effective gid)
// connects and exchanges a line; a stranger (uid 61002) is refused by the
// directory, then — the directory opened to 0755 — by the socket's 0660 mode.
// With AllowUIDs [61001] instead the socket is 0666, the member still
// exchanges its line, and the stranger reaches the listener and is refused
// there: PEER_REFUSED, Refused() == 1. Before #1092 the socket was 0600 and
// the member was refused by the kernel (DIAL_FAILED); a supplementary group
// was never read.
//
// Unprivileged it skips: a second account needs root — a user namespace
// mapping one uid keeps the kernel's uid, so SO_PEERCRED and the socket's
// mode would see the same account. TestTheSocketModeFollowsTheAllowLists,
// TestAdmit and TestPeerGroupsAreTheKernels pin the same rules unprivileged;
// the linux-init lane runs this one as root.
func TestAnAllowedAccountExchangesALineAndAStrangerIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("two accounts need root; TestTheSocketModeFollowsTheAllowLists and TestAdmit pin the rules unprivileged")
	}
	tmp := t.TempDir()
	run := filepath.Join(tmp, "run")
	path := filepath.Join(run, "d.sock")
	if len(path) > 103 {
		t.Skipf("the test's directory is too deep for a socket path (%d bytes)", len(path))
	}
	openToSearch(t, tmp)
	binary := helperBinary(t, tmp)
	if err := os.Mkdir(run, 0o750); err != nil {
		t.Fatal(err)
	}
	//: Mkdir's mode passes through the umask; the group's search is the row.
	if err := os.Chmod(run, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(run, daemonUID, oncallGID); err != nil {
		t.Fatal(err)
	}
	member := account{memberUID, memberUID, []int{oncallGID}}
	stranger := account{strangerUID, strangerUID, nil}

	d := startDaemon(t, binary, tmp, path, nil, []int{oncallGID})
	if got := dialAs(t, binary, tmp, path, member); got != "ok" {
		t.Fatalf("the group's member (supplementary) = %q, want ok", got)
	}
	if got := dialAs(t, binary, tmp, path, stranger); !strings.Contains(got, "DIAL_FAILED") {
		t.Fatalf("a stranger through the 0750 directory = %q, want DIAL_FAILED", got)
	}
	if err := os.Chmod(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := dialAs(t, binary, tmp, path, stranger); !strings.Contains(got, "DIAL_FAILED") {
		t.Fatalf("a stranger at the 0660 socket = %q, want DIAL_FAILED", got)
	}
	d.stop(t)

	d = startDaemon(t, binary, tmp, path, []int{memberUID}, nil)
	if got := dialAs(t, binary, tmp, path, member); got != "ok" {
		t.Fatalf("the allowed user = %q, want ok", got)
	}
	if got := dialAs(t, binary, tmp, path, stranger); !strings.HasPrefix(got, "eof") {
		t.Fatalf("a stranger at the 0666 socket = %q, want the listener to close it unanswered", got)
	}
	if line := d.next(t); line != "refused 1" {
		t.Fatalf("the daemon said %q, want refused 1", line)
	}
}
