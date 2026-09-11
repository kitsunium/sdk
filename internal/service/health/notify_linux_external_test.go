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
	"sync/atomic"
	"testing"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
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
