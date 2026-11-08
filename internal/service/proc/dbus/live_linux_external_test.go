//go:build linux

package dbus_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/dbus"
)

// The client against a real dbus-daemon the test launches: its socket in the
// test's directory, its configuration written there — never the system bus,
// never the user's session bus. Without dbus-daemon on PATH the test skips.

// Conn is a proc/service BusWatcher as it is.
var _ coresvc.BusWatcher = (*dbus.Conn)(nil)

// busConfig is a session-type bus that admits every connection of this user
// to own and watch any name, and listens where the test says.
const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=SOCKET</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// startBus launches a dbus-daemon of the test's own and returns its address;
// the daemon is killed when the test ends.
func startBus(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon on PATH: the real bus is not tested here")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	socket := filepath.Join(dir, "bus")
	if err := os.WriteFile(conf, []byte(strings.Replace(busConfig, "SOCKET", socket, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config-file="+conf, "--nofork", "--nopidfile", "--nosyslog", "--print-address=1")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(out).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case address := <-line:
		if !strings.HasPrefix(address, "unix:path="+socket) {
			t.Fatalf("dbus-daemon listens at %q, want under the test's directory", address)
		}
		return address
	case <-time.After(20 * time.Second):
		t.Fatal("dbus-daemon printed no address within 20 s")
		return ""
	}
}

// dial connects to the test's bus.
func dial(t *testing.T, address string) *dbus.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dbus.NewConn(ctx, address)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// next reads one owner from a watch, failing after ten seconds.
func next(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("the watch ended")
		}
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("no owner within 10 s")
		return ""
	}
}

// TestRealBusWatch: against dbus-daemon, a watch sees the owner when it
// began, every connection that takes the name, its release and its owner's
// disconnection; RequestName never queues; the watch ends with its context.
func TestRealBusWatch(t *testing.T) {
	address := startBus(t)
	const name = "org.kitsunium.Test.Watch"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	watcher := dial(t, address)
	if !strings.HasPrefix(watcher.UniqueName(), ":") {
		t.Fatalf("unique name %q", watcher.UniqueName())
	}
	wctx, stop := context.WithCancel(ctx)
	owners, err := watcher.WatchName(wctx, name)
	if err != nil {
		t.Fatalf("WatchName: %v", err)
	}
	if v := next(t, owners); v != "" {
		t.Fatalf("first owner %q, want none", v)
	}
	first, second := dial(t, address), dial(t, address)
	if ok, err := first.RequestName(ctx, name); !ok || err != nil {
		t.Fatalf("RequestName: %v %v", ok, err)
	}
	if v := next(t, owners); v != first.UniqueName() {
		t.Fatalf("owner %q, want %q", v, first.UniqueName())
	}
	if ok, err := first.RequestName(ctx, name); !ok || err != nil {
		t.Fatalf("RequestName of one's own name: %v %v", ok, err)
	}
	if ok, err := second.RequestName(ctx, name); ok || err != nil {
		t.Fatalf("RequestName of another's name: %v %v, want false, never queued", ok, err)
	}
	if got, err := second.NameOwner(ctx, name); err != nil || got != first.UniqueName() {
		t.Fatalf("NameOwner: %q %v", got, err)
	}
	if ok, err := second.ReleaseName(ctx, name); ok || err != nil {
		t.Fatalf("ReleaseName of another's name: %v %v", ok, err)
	}
	if ok, err := first.ReleaseName(ctx, name); !ok || err != nil {
		t.Fatalf("ReleaseName: %v %v", ok, err)
	}
	if v := next(t, owners); v != "" {
		t.Fatalf("after the release %q, want none", v)
	}
	if ok, err := second.RequestName(ctx, name); !ok || err != nil {
		t.Fatalf("RequestName after the release: %v %v", ok, err)
	}
	if v := next(t, owners); v != second.UniqueName() {
		t.Fatalf("owner %q, want %q", v, second.UniqueName())
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if v := next(t, owners); v != "" {
		t.Fatalf("after its owner went %q, want none", v)
	}
	stop()
	select {
	case _, ok := <-owners:
		if ok {
			t.Fatal("an owner after the watch's context ended")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not end with its context")
	}
	if got, err := watcher.NameOwner(ctx, name); err != nil || got != "" {
		t.Fatalf("NameOwner of nobody's name: %q %v", got, err)
	}
}

// TestRealBusOwnedBeforeTheWatch: a name owned when the watch begins is its
// first value.
func TestRealBusOwnedBeforeTheWatch(t *testing.T) {
	address := startBus(t)
	const name = "org.kitsunium.Test.Before"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, watcher := dial(t, address), dial(t, address)
	if ok, err := owner.RequestName(ctx, name); !ok || err != nil {
		t.Fatalf("RequestName: %v %v", ok, err)
	}
	owners, err := watcher.WatchName(ctx, name)
	if err != nil {
		t.Fatalf("WatchName: %v", err)
	}
	if v := next(t, owners); v != owner.UniqueName() {
		t.Fatalf("first owner %q, want %q", v, owner.UniqueName())
	}
}
