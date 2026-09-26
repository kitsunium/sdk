// Package health_test — Ask as a consumer meets it: the probe a binary runs as
// its own HEALTHCHECK, asking the readiness handler the same package serves.
package health_test

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/health"
)

// TestAskAsksWhatTheReadinessHandlerAnswers is the domain's two halves meeting:
// a registry served over loopback answers ready, then — after Drain — answers
// 503, which Ask reports as AskNotReady with the status and nothing else.
func TestAskAsksWhatTheReadinessHandlerAnswers(t *testing.T) {
	t.Parallel()
	registry := health.New(health.Config{})
	server := httptest.NewServer(health.NewReadinessHandler(registry, health.HandlerConfig{Detail: true}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	//: httptest always binds a host:port.
	if err != nil {
		t.Fatalf("server address: %v", err)
	}
	// ":port" is how the process was told to listen; Ask dials the loopback.
	cfg := health.AskConfig{Addr: ":" + port, Path: "/readyz"}

	status, err := health.Ask(t.Context(), cfg)
	//: a registry with no checks is ready.
	if status != http.StatusOK || err != nil {
		t.Fatalf("Ask() = %d, %v; want 200 and nil", status, err)
	}

	registry.Drain()
	status, err = health.Ask(t.Context(), cfg)
	//: draining is not ready, and says so by code.
	if status != http.StatusServiceUnavailable || !errors.Is(err, health.AskNotReady) {
		t.Fatalf("after Drain, Ask() = %d, %v; want 503 and ASK_NOT_READY", status, err)
	}
	code, _ := errs.CodeOf(health.AskNotReady)
	//: the code is readable through the public errs accessors.
	if !errs.HasCode(err, code) {
		t.Errorf("errs.HasCode lost the code of %v", err)
	}
}

// TestAskSentinelsAreTheEnginesOwn pins that the facade re-exports the
// engine's values rather than copies: a refusal from Ask matches them.
func TestAskSentinelsAreTheEnginesOwn(t *testing.T) {
	t.Parallel()
	_, err := health.Ask(t.Context(), health.AskConfig{Addr: "no-port", Path: "/readyz"})
	//: a copy would not match.
	if !errors.Is(err, health.AskMisconfigured) {
		t.Fatalf("Ask() error = %v, want ASK_MISCONFIGURED", err)
	}
	//: each sentinel carries its own code.
	for _, sentinel := range []error{health.AskMisconfigured, health.AskUnreachable, health.AskTimeout, health.AskNotReady} {
		//: a code of its own, readable through errs.
		if code, ok := errs.CodeOf(sentinel); !ok || code == 0 {
			t.Errorf("%v carries no code", sentinel)
		}
	}
}
