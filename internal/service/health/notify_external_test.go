// Package health_test — the opt-in sd_notify wiring.
package health_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// TestNotifyIsSilentWithoutASupervisor pins that setting Notify on a process
// nobody supervises is safe.
//
// With $NOTIFY_SOCKET unset the notifier is a documented no-op that returns
// nil, so a binary that runs both under systemd and from a shell can leave the
// field on. If this regressed, every probe on an unsupervised process would
// hand an error to OnNotifyError, and the field would become one nobody dares
// set.
func TestNotifyIsSilentWithoutASupervisor(t *testing.T) {
	//: not parallel — $NOTIFY_SOCKET is process-wide, and this case is about
	//: its absence.
	t.Setenv("NOTIFY_SOCKET", "")
	var notifyErrors atomic.Int64
	registry, _ := newRegistry(t, svchealth.Config{
		Notify:        true,
		OnNotifyError: func(error) { notifyErrors.Add(1) },
	})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{Name: "db", Check: passing(&calls)})
	for range 3 {
		registry.Probe(context.Background(), corehealth.ProbeReadiness)
	}
	if got := notifyErrors.Load(); got != 0 {
		t.Errorf("%d notification errors on an unsupervised process, want 0", got)
	}
}

// TestObservationCarriesWhatTheBodyDoesNot pins the other half of the
// public/private split: the hook — which is the caller's own log — sees the
// full error, including the Private half the handler must never render.
func TestObservationCarriesWhatTheBodyDoesNot(t *testing.T) {
	t.Parallel()
	var seen atomic.Int64
	var lastErr atomic.Value
	registry, _ := newRegistry(t, svchealth.Config{
		OnReport: func(report corehealth.ReportValue) {
			seen.Add(1)
			for _, result := range report.Results {
				if result.Err != nil {
					lastErr.Store(result.Err)
				}
			}
		},
	})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: failingWith(&calls, errDependency),
	})
	registry.Probe(context.Background(), corehealth.ProbeReadiness)
	if got := seen.Load(); got != 1 {
		t.Errorf("the hook saw %d reports, want 1", got)
	}
	stored, ok := lastErr.Load().(error)
	if !ok {
		t.Fatal("the hook saw no error at all")
	}
	//: the caller's original error is still IN the chain, so their errors.Is
	//: keeps working and their logger can read every layer of it. Redaction
	//: belongs at the handler, which is the only place a stranger reads.
	if !errors.Is(stored, errDependency) {
		t.Errorf("the hook was handed %v; the caller's own error must survive verbatim", stored)
	}
}
