//go:build linux

package service_test

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/service/proc/service"
	svcnotify "github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// helperEnv makes the test binary a notify service instead of the tests.
const helperEnv = "PROC_SERVICE_TEST_HELPER"

// helpers are the services this binary plays when the engine spawned it, by
// the value of helperEnv; each test file adds its own.
var helpers = map[string]func(){"notify": notifyHelper}

// TestMain runs the helper when this binary was spawned to play one, once the
// guard admitted it (helperguard_linux_external_test.go).
func TestMain(m *testing.M) {
	if name, ok := os.LookupEnv(helperEnv); ok {
		admitHelper(name)()
		return
	}
	refuseLostRole()
	os.Exit(m.Run())
}

// notifyHelper sends READY=1 and STATUS=, then waits for SIGTERM.
func notifyHelper() {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	if err := svcnotify.Notify(map[string]string{"READY": "1", "STATUS": "helper up"}); err != nil {
		os.Exit(2)
	}
	<-term
	os.Exit(0)
}

// TestRealNotify: a Type=notify unit over ListenNotify is up once its process
// sent READY=1 on the socket the engine gave it, its STATUS= recorded.
func TestRealNotify(t *testing.T) {
	src, err := service.ListenNotify()
	if err != nil {
		t.Fatalf("ListenNotify: %v", err)
	}
	u := realUnit(t, service.Config{
		Type: coresvc.TypeNotify, Notify: src,
		Spec:      coreproc.Spec{Env: helperEnviron("notify")},
		ExecStart: []coresvc.CommandValue{command(t, os.Args[0], "-test.run=^$")},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if st := u.State(); st.State != coresvc.StateActive || st.Status != "helper up" {
		t.Fatalf("after READY=1: %s %q", st.State, st.Status)
	}
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
