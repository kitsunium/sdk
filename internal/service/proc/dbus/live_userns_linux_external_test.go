//go:build linux

package dbus_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/proc/dbus"
)

// A client in a user namespace — a sandboxed service's (S2, #728), root there
// mapped to the test's user — against the test's own dbus-daemon (startBus):
// the bus knows it as the user the kernel names as the socket's peer, never
// as the uid of its namespace. This test binary is that client, re-executed
// in a new user namespace; it skips where the kernel refuses one.

const (
	// userNSAddressEnv makes the test binary the client in a user namespace,
	// connecting to the bus at its value; userNSNameEnv is the name it owns.
	userNSAddressEnv = "PROC_DBUS_TEST_USERNS_BUS"
	userNSNameEnv    = "PROC_DBUS_TEST_USERNS_NAME"
)

func TestMain(m *testing.M) {
	if address := os.Getenv(userNSAddressEnv); address != "" {
		os.Exit(ownFromHere(address, os.Getenv(userNSNameEnv)))
	}
	os.Exit(m.Run())
}

// ownFromHere connects to the bus at address, owns name, says so on stdout
// and holds it until its stdin closes: 0, or 2 when the bus refused the
// connection, 3 the name.
func ownFromHere(address, name string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := dbus.NewConn(ctx, address)
	if err != nil {
		fmt.Println("NewConn:", err)
		return 2
	}
	defer c.Close()
	if ok, err := c.RequestName(ctx, name); err != nil || !ok {
		fmt.Println("RequestName:", ok, err)
		return 3
	}
	fmt.Printf("uid %d owns %s\n", os.Getuid(), name)
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// TestRealBusFromAUserNamespace: a client whose uid is 0 in its own user
// namespace connects and owns a name, which a watcher outside sees owned,
// then free once the client ended. AUTH EXTERNAL naming that uid would be
// REJECTED: the bus sees the test's user.
func TestRealBusFromAUserNamespace(t *testing.T) {
	address := startBus(t)
	const name = "org.kitsunium.Test.UserNamespace"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owners, err := dial(t, address).WatchName(ctx, name)
	if err != nil {
		t.Fatalf("WatchName: %v", err)
	}
	if v := next(t, owners); v != "" {
		t.Fatalf("first owner %q, want none", v)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), userNSAddressEnv+"="+address, userNSNameEnv+"="+name)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		for _, refused := range []error{syscall.EPERM, syscall.EACCES, syscall.ENOSPC, syscall.EINVAL, syscall.ENOSYS} {
			if errors.Is(err, refused) {
				t.Skipf("the kernel refuses this test a user namespace: %v", err)
			}
		}
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	select {
	case v, ok := <-owners:
		if !ok || v == "" {
			t.Fatalf("owner %q (watch open %v), want the client's connection", v, ok)
		}
	case werr := <-done:
		done <- werr
		t.Fatalf("the client in a user namespace ended before owning %s: %v\n%s", name, werr, out.String())
	case <-time.After(20 * time.Second):
		t.Fatal("no owner within 20 s")
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	werr := <-done
	done <- werr
	if werr != nil || !strings.HasPrefix(out.String(), "uid 0 owns "+name) {
		t.Fatalf("the client: %v, said %q; want uid 0 in its namespace", werr, out.String())
	}
	if v := next(t, owners); v != "" {
		t.Fatalf("after the client ended %q, want none", v)
	}
}
