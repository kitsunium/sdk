//go:build linux

// Package health_test — the half of the sd_notify wiring that needs a real
// datagram socket.
//
// It is constrained to linux because that is where internal/service/proc/
// sdnotify can stand a listener up: the credential-verified receive path needs
// SO_PASSCRED, which is a Linux mechanism. Elsewhere the wiring is still
// COMPILED and its silence is covered by the portable file next to this one
// (ADR 0018 — the runtime bar is met where the mechanism exists, not pretended
// where it does not).
package health_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
	svcsdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// TestReadyIsAnnouncedOnceWhenTheReplicaCanFirstServe pins the whole opt-in
// wiring against a real supervisor socket.
//
// Three properties in one flight, because they are only meaningful together:
// nothing is sent while the replica cannot serve, READY=1 is sent the first
// time it can, and it is sent ONCE — a READY re-sent on every poll would make
// the supervisor's journal useless and would hide the transition that matters.
//
// It is deliberately driven by READINESS. "Every component constructed" and
// "this replica can answer a request" are different claims, and a supervisor
// that acts on the first while the second is false routes traffic to a process
// that cannot serve it.
func TestReadyIsAnnouncedOnceWhenTheReplicaCanFirstServe(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide.
	listener, socketPath, err := svcsdnotify.Listen()
	if err != nil {
		t.Fatalf("Listen = %v, want nil", err)
	}
	t.Cleanup(func() {
		//: a socket this test could not close is a leak the next test would
		//: inherit, not a detail — so it is reported rather than dropped.
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("listener.Close = %v, want nil", closeErr)
		}
	})
	t.Setenv("NOTIFY_SOCKET", socketPath)

	var notifyErrors atomic.Int64
	registry, _ := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(error) { notifyErrors.Add(1) },
	})
	var calls atomic.Int64
	failing := true
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: func(_ context.Context) error {
			calls.Add(1)
			if failing {
				return errDependency
			}
			return nil
		},
	})
	ctx := context.Background()
	//: a not-serving aggregate announces NOTHING: a unit that has never been
	//: ready is what the supervisor already assumes.
	registry.Probe(ctx, corehealth.ProbeReadiness)

	failing = false
	//: two serving probes, so a second READY would be observable.
	registry.Probe(ctx, corehealth.ProbeReadiness)
	registry.Probe(ctx, corehealth.ProbeReadiness)

	notification := recvOne(t, listener)
	if !notification.Ready() {
		t.Fatalf("the first datagram is %v, want READY=1", notification.State)
	}
	//: the second serving probe must have sent nothing, so the NEXT datagram
	//: on this socket is the one this test sends by hand.
	if err := svcsdnotify.Status("sentinel"); err != nil {
		t.Fatalf("Status = %v, want nil", err)
	}
	if got := recvOne(t, listener); got.Status != "sentinel" {
		t.Errorf("the next datagram is %v, want the sentinel — READY must be sent "+
			"once, not on every poll", got.State)
	}
	if got := notifyErrors.Load(); got != 0 {
		t.Errorf("%d notification errors against a real socket, want 0", got)
	}
}

// recvOne reads one notification or fails the test.
func recvOne(tb testing.TB, listener coreproc.Listener) coreproc.NotificationValue {
	tb.Helper()
	notification, err := listener.Recv()
	if err != nil {
		tb.Fatalf("Recv = %v, want nil", err)
	}
	return notification
}

// supervisorSocket stands up a real notify socket for the test's lifetime and
// returns it with the $NOTIFY_SOCKET value that reaches it.
func supervisorSocket(tb testing.TB) (coreproc.Listener, string) {
	tb.Helper()
	listener, socketPath, err := svcsdnotify.Listen()
	if err != nil {
		tb.Fatalf("Listen = %v, want nil", err)
	}
	tb.Cleanup(func() {
		//: a socket this test could not close is a leak the next test would
		//: inherit, so it is reported rather than dropped.
		if closeErr := listener.Close(); closeErr != nil {
			tb.Errorf("listener.Close = %v, want nil", closeErr)
		}
	})
	return listener, socketPath
}

// switchableReadiness registers one readiness check whose verdict the test
// flips, and returns the switch.
func switchableReadiness(tb testing.TB, registry corehealth.Health) *atomic.Bool {
	tb.Helper()
	var serving atomic.Bool
	serving.Store(true)
	mustAddReadiness(tb, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: func(_ context.Context) error {
			if serving.Load() {
				return nil
			}
			return errDependency
		},
	})
	return &serving
}

// TestAFailedReadyIsSentAgain pins that READY=1 counts as announced only once
// it has been DELIVERED.
//
// A Type=notify unit waits for READY=1 and is killed at TimeoutStartSec if it
// never comes. When the announced state was committed BEFORE the send, one
// failed datagram meant READY was never attempted again, and the unit was
// killed while every probe said the replica could serve. The step in between
// matters as much: after a READY nobody received, a not-serving probe must
// still say NOTHING, since the supervisor never heard the claim a STATUS line
// would regress from.
//
// MUTATION (2026-09-11): HEAD's notify.go put back, which records the state
// before sending. Observed: `the first datagram delivered is
// map[STATUS:health: unhealthy], want READY=1 — a READY that failed was never
// sent again`: the failed READY had been recorded as sent, so the not-serving
// probe sent a STATUS line to a supervisor that never saw READY, and READY
// itself never went out. Restored; SHA-256 of notify.go identical to the fixed
// file.
func TestAFailedReadyIsSentAgain(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide.
	listener, socketPath := supervisorSocket(t)
	//: first a socket path nothing listens on, so the first READY fails.
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	var failures []error
	registry, _ := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(err error) { failures = append(failures, err) },
	})
	serving := switchableReadiness(t, registry)
	ctx := context.Background()
	registry.Probe(ctx, corehealth.ProbeReadiness)
	if len(failures) != 1 || !errs.HasCode(failures[0], svchealth.CodeNotifyFailed) {
		t.Fatalf("a READY=1 nobody received: the hook saw %v, want one NOTIFY_FAILED", failures)
	}
	//: the supervisor is reachable from here on.
	t.Setenv("NOTIFY_SOCKET", socketPath)
	//: not serving: still nothing is owed, neither READY nor STATUS.
	serving.Store(false)
	registry.Probe(ctx, corehealth.ProbeReadiness)
	//: serving again: READY=1 is still owed, and is sent — once.
	serving.Store(true)
	registry.Probe(ctx, corehealth.ProbeReadiness)
	registry.Probe(ctx, corehealth.ProbeReadiness)
	if err := svcsdnotify.Status("sentinel"); err != nil {
		t.Fatalf("Status = %v, want nil", err)
	}
	if got := recvOne(t, listener); !got.Ready() {
		t.Fatalf("the first datagram delivered is %v, want READY=1 — a READY that failed was never sent again", got.State)
	}
	if got := recvOne(t, listener); got.Status != "sentinel" {
		t.Errorf("the next datagram is %v, want the sentinel — READY is owed once, not per poll", got.State)
	}
	if len(failures) != 1 {
		t.Errorf("%d notification errors, want exactly the first one", len(failures))
	}
}

// TestAFailedStatusIsSentAgain is the same rule for the STATUS= line after
// READY. A change whose datagram failed is still owed: when the new aggregate
// was recorded before its send, the supervisor kept showing the OLD status —
// `systemctl status` reporting a healthy replica that was not — until the
// aggregate happened to change again.
//
// MUTATION (2026-09-11): HEAD's notify.go put back. Observed: `the datagram
// after READY is map[STATUS:sentinel], want STATUS=health: unhealthy — a
// status line that failed was never sent again`. Restored; SHA-256 of
// notify.go identical to the fixed file.
func TestAFailedStatusIsSentAgain(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide.
	listener, socketPath := supervisorSocket(t)
	t.Setenv("NOTIFY_SOCKET", socketPath)
	var failures atomic.Int64
	registry, _ := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(error) { failures.Add(1) },
	})
	serving := switchableReadiness(t, registry)
	ctx := context.Background()
	//: READY=1, delivered.
	registry.Probe(ctx, corehealth.ProbeReadiness)
	//: the supervisor is unreachable exactly when the aggregate changes.
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	serving.Store(false)
	registry.Probe(ctx, corehealth.ProbeReadiness)
	if got := failures.Load(); got != 1 {
		t.Fatalf("%d notification errors for the undeliverable STATUS, want 1", got)
	}
	//: reachable again, same aggregate: the failed line is still owed.
	t.Setenv("NOTIFY_SOCKET", socketPath)
	registry.Probe(ctx, corehealth.ProbeReadiness)
	if err := svcsdnotify.Status("sentinel"); err != nil {
		t.Fatalf("Status = %v, want nil", err)
	}
	if got := recvOne(t, listener); !got.Ready() {
		t.Fatalf("the first datagram is %v, want READY=1", got.State)
	}
	if got := recvOne(t, listener); got.Status != "health: unhealthy" {
		t.Fatalf("the datagram after READY is %v, want STATUS=health: unhealthy — "+
			"a status line that failed was never sent again", got.State)
	}
	if got := recvOne(t, listener); got.Status != "sentinel" {
		t.Errorf("the next datagram is %v, want the sentinel", got.State)
	}
}
