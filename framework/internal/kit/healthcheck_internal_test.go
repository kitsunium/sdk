package kit

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
)

// healthcheck answers 0 when the product on KIT_ADDR is ready, and 1 with
// the reason on stderr when nothing listens there — an unspecified host
// dials this machine.
func TestTheHealthcheckAsksTheRunningProduct(t *testing.T) {
	svc := NewService("probed", "")
	app := NewApp("probed", svc).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	u, uErr := url.Parse(app.URL())
	if uErr != nil {
		t.Fatal(uErr)
	}
	_, port, portErr := net.SplitHostPort(u.Host)
	if portErr != nil {
		t.Fatal(portErr)
	}
	probe := NewApp("probed", NewService("probed", ""))
	var stderr strings.Builder
	for _, addr := range []string{u.Host, ":" + port, "0.0.0.0:" + port} {
		t.Setenv("KIT_ADDR", addr)
		if code := probe.healthcheck(t.Context(), &stderr); code != 0 {
			t.Errorf("KIT_ADDR=%s: healthcheck = %d: %s", addr, code, stderr.String())
		}
	}

	closed, closedErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if closedErr != nil {
		t.Fatal(closedErr)
	}
	gone := closed.Addr().String()
	closed.Close()
	stderr.Reset()
	t.Setenv("KIT_ADDR", gone)
	if code := probe.healthcheck(t.Context(), &stderr); code != 1 || !strings.HasPrefix(stderr.String(), "probed is not ready: ") || len(stderr.String()) < len("probed is not ready: x") {
		t.Errorf("nothing listening: healthcheck = %d, stderr %q", code, stderr.String())
	}
}
