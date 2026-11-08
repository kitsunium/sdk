//go:build linux

package service_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/dbus"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// Type=dbus (S0e) against a real dbus-daemon the test launches — its socket
// and configuration under t.TempDir(), never the system bus or the user's
// session bus —, the unit's main process this test binary owning its name
// through proc/dbus. Without dbus-daemon on PATH the tests skip.

const (
	// busAddressEnv and busNameEnv tell the helper which bus and which name.
	busAddressEnv = "PROC_SERVICE_TEST_BUS"
	busNameEnv    = "PROC_SERVICE_TEST_BUS_NAME"
)

func init() {
	helpers["dbus"] = dbusHelper
	helpers["dbus-silent"] = silentHelper
}

// dbusHelper owns the name on the bus, gives it back on SIGUSR1, and ends on
// SIGTERM.
func dbusHelper() {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGUSR1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := dbus.NewConn(ctx, os.Getenv(busAddressEnv))
	if err != nil {
		os.Exit(2)
	}
	defer conn.Close()
	name := os.Getenv(busNameEnv)
	if owned, err := conn.RequestName(ctx, name); err != nil || !owned {
		os.Exit(3)
	}
	for sig := range sigs {
		if sig == syscall.SIGTERM {
			return
		}
		_, _ = conn.ReleaseName(ctx, name)
	}
}

// silentHelper never owns a name: it waits for SIGTERM.
func silentHelper() {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	<-term
}

// busConf is a session-type bus that admits this user to own and watch any
// name, listening where the test says.
const busConf = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
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

// testBus launches a dbus-daemon of the test's own — killed when the test
// ends — and returns its address and a connection to it.
func testBus(t *testing.T) (string, *dbus.Conn) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon on PATH: Type=dbus over a real bus is not tested here")
	}
	dir := t.TempDir()
	conf, socket := filepath.Join(dir, "bus.conf"), filepath.Join(dir, "bus")
	if err := os.WriteFile(conf, []byte(strings.Replace(busConf, "SOCKET", socket, 1)), 0o600); err != nil {
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
	var address string
	select {
	case address = <-line:
	case <-time.After(20 * time.Second):
		t.Fatal("dbus-daemon printed no address within 20 s")
	}
	if !strings.HasPrefix(address, "unix:path="+socket) {
		t.Fatalf("dbus-daemon listens at %q, want under the test's directory", address)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dbus.NewConn(ctx, address)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return address, conn
}

// TestRealTypeDbus: a Type=dbus unit whose main process owns its name on a
// real bus is up once it does — the owner the bus names is that process's
// connection —; the name given back stops the unit, KillSignal= ending the
// process, with the result success.
func TestRealTypeDbus(t *testing.T) {
	address, conn := testBus(t)
	const name = "org.kitsunium.Test.Unit"
	u := realUnit(t, service.Config{
		Type: coresvc.TypeDbus, BusName: name, BusWatcher: conn,
		Spec:      coreproc.Spec{Env: helperEnviron("dbus", busAddressEnv+"="+address, busNameEnv+"="+name)},
		ExecStart: []coresvc.CommandValue{command(t, os.Args[0], "-test.run=^$")},
	})
	sub, cancelSub := u.Subscribe(64)
	defer cancelSub()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := u.State()
	if st.State != coresvc.StateActive || st.Sub != coresvc.SubRunning || st.MainPID <= 0 {
		t.Fatalf("after Start: %s/%s main %d", st.State, st.Sub, st.MainPID)
	}
	if owner, err := conn.NameOwner(ctx, name); err != nil || owner == "" {
		t.Fatalf("the name's owner: %q %v", owner, err)
	}
	// The unit's own main process, which this test spawned through it: told
	// to give its name back.
	if err := syscall.Kill(st.MainPID, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	ev := waitState(t, sub, coresvc.StateInactive)
	if ev.Result != coresvc.ResultSuccess {
		t.Fatalf("after the name was given back: result %s, want success", ev.Result)
	}
}

// TestRealTypeDbusNeverOwned: a main process that never owns the name fails
// the start after TimeoutStartSec=, by timeout, the process terminated.
func TestRealTypeDbusNeverOwned(t *testing.T) {
	_, conn := testBus(t)
	u := realUnit(t, service.Config{
		Type: coresvc.TypeDbus, BusName: "org.kitsunium.Test.Never", BusWatcher: conn, TimeoutStart: 2 * time.Second,
		Spec:      coreproc.Spec{Env: helperEnviron("dbus-silent")},
		ExecStart: []coresvc.CommandValue{command(t, os.Args[0], "-test.run=^$")},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := u.Start(ctx)
	if !errs.HasCode(err, coresvc.CodeStartFailed) || u.State().Result != coresvc.ResultTimeout {
		t.Fatalf("Start: %v (result %s), want START_FAILED by timeout", err, u.State().Result)
	}
}
