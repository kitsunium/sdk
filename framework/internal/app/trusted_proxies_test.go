package app_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Edge is a product behind a proxy: one endpoint that answers who it thinks
// the caller is, and a bucket per client in front of it.
var Edge = app.NewService("edge", "A product behind a proxy, for the tests.")

// EdgeWho is the caller as the endpoint sees it.
type EdgeWho struct {
	IP string `json:"ip"`
}

var _ = Edge.Endpoint("GET /who", func(ctx context.Context, _ app.EmptyValue) (EdgeWho, error) {
	return EdgeWho{IP: app.ClientIP(ctx)}, nil
}, app.RateLimitPerClient(0.001, 1))

// startEdge runs Edge under the environment the caller set.
func startEdge(t *testing.T) (*app.App, error) {
	t.Helper()
	a := app.NewApp("edge", Edge).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard))
	if err := a.Start(t.Context()); err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a, nil
}

// who asks /who with the headers given and answers the status and the
// address the product saw.
func who(t *testing.T, a *app.App, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, a.URL()+"/who", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// #1141: a peer that is not one of the proxies the operator listed is not
// believed. The test client reaches the product from 127.0.0.1, which the
// list does not name, so whatever it forges, the address stays its own and a
// bucket of one is empty at the second call.
func TestAnUntrustedPeerCannotForgeItsAddress(t *testing.T) {
	t.Setenv("EDGE_TRUST_PROXY", "on")
	t.Setenv("EDGE_TRUSTED_PROXIES", "10.0.0.0/8")
	a, err := startEdge(t)
	if err != nil {
		t.Fatal(err)
	}
	status, body := who(t, a, "X-Forwarded-For", "6.6.6.6")
	if status != http.StatusOK || !strings.Contains(body, `"127.0.0.1"`) {
		t.Fatalf("the first call answered %d %s, want 200 and the peer's address 127.0.0.1", status, body)
	}
	// The same peer with another forged address and a second header line: one
	// bucket, because the client is one address.
	status, _ = who(t, a, "X-Forwarded-For", "7.7.7.7", "X-Forwarded-For", "8.8.8.8")
	if status != http.StatusTooManyRequests {
		t.Fatalf("a second call from one untrusted peer answered %d, want 429 whatever headers it carries", status)
	}
}

// The peer is one of the listed proxies: the chain is walked from its right.
func TestATrustedPeerIsBelievedFromTheRight(t *testing.T) {
	t.Setenv("EDGE_TRUST_PROXY", "on")
	t.Setenv("EDGE_TRUSTED_PROXIES", "127.0.0.0/8, ::1")
	a, err := startEdge(t)
	if err != nil {
		t.Fatal(err)
	}
	// The client forged its first hop, then the proxy at 127.0.0.2 appended
	// the address the connection came from.
	status, body := who(t, a, "X-Forwarded-For", "6.6.6.6, 203.0.113.9, 127.0.0.2")
	if status != http.StatusOK || !strings.Contains(body, `"203.0.113.9"`) {
		t.Fatalf("answered %d %s, want the first hop outside the proxies, 203.0.113.9", status, body)
	}
}

// TRUST_PROXY=on without the list of proxies is a start that names the
// variable it lacks; a list that is not one names it too; and a list
// alone, without the switch, trusts nobody.
func TestTheTrustedProxiesAreRequiredAndChecked(t *testing.T) {
	for _, c := range []struct {
		name, trust, list, reason string
	}{
		{"on without a list", "on", "", "TRUSTED_PROXIES_REQUIRED"},
		{"on with an empty list", "on", " , ", "TRUSTED_PROXIES_REQUIRED"},
		{"a list that is not addresses", "on", "10.0.0.0/8, not-a-network", "TRUSTED_PROXIES_INVALID"},
		{"a prefix past the address", "on", "10.0.0.0/33", "TRUSTED_PROXIES_INVALID"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("EDGE_TRUST_PROXY", c.trust)
			t.Setenv("EDGE_TRUSTED_PROXIES", c.list)
			_, err := startEdge(t)
			if err == nil {
				t.Fatal("the start was not refused")
			}
			if !errs.HasCode(err, app.CodeAppConfig) || !errs.HasReason(err, c.reason) {
				t.Errorf("refused with %v, want %s under CodeAppConfig", err, c.reason)
			}
			if !strings.Contains(errs.PublicOf(err), "EDGE_TRUSTED_PROXIES") {
				t.Errorf("the refusal does not name EDGE_TRUSTED_PROXIES: %q", errs.PublicOf(err))
			}
		})
	}
	t.Run("a list without the switch trusts nobody", func(t *testing.T) {
		t.Setenv("EDGE_TRUST_PROXY", "")
		t.Setenv("EDGE_TRUSTED_PROXIES", "127.0.0.0/8")
		a, err := startEdge(t)
		if err != nil {
			t.Fatal(err)
		}
		if status, body := who(t, a, "X-Forwarded-For", "6.6.6.6"); status != http.StatusOK || !strings.Contains(body, `"127.0.0.1"`) {
			t.Fatalf("answered %d %s, want the peer's own address: the switch is off", status, body)
		}
	})
}
