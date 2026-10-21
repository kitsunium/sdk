// Package client_test — a server's Retry-After, carried for the retry policy.
package client_test

import (
	"net/http"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// TestClient_Get_CarriesRetryAfter pins #476's client half: a 429 or a 503
// whose Retry-After holds delta-seconds is returned with a CallFailed that
// resilience.RetryAfter reads, so a RetryConfig.Delay honours the server's
// hint with no adapter. Every other shape carries no delay: an HTTP-date (the
// server's clock is not ours), a status the header does not mean "come back
// later" on, and a value that is not a positive whole number.
func TestClient_Get_CarriesRetryAfter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		status     int
		retryAfter string
		want       time.Duration
		wantOK     bool
	}
	tests := []tc{
		{name: "a 429 with seconds", status: http.StatusTooManyRequests, retryAfter: "7", want: 7 * time.Second, wantOK: true},
		{name: "a 503 with seconds", status: http.StatusServiceUnavailable, retryAfter: " 120 ", want: 2 * time.Minute, wantOK: true},
		{name: "a 429 with an HTTP-date", status: http.StatusTooManyRequests, retryAfter: "Wed, 21 Oct 2026 07:28:00 GMT"},
		{name: "a 429 with zero", status: http.StatusTooManyRequests, retryAfter: "0"},
		{name: "a 429 with a negative value", status: http.StatusTooManyRequests, retryAfter: "-3"},
		{name: "a 429 without the header", status: http.StatusTooManyRequests},
		{name: "a 502 with seconds", status: http.StatusBadGateway, retryAfter: "7"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
				if c.retryAfter != "" {
					w.Header().Set("Retry-After", c.retryAfter)
				}
				w.WriteHeader(c.status)
			})
			_, err := newClient(t, srv, corenet.ClientConfig{}).Get(t.Context(), "/v1/subscribers", nil)
			if !errs.HasCode(err, corenet.CodeCallFailed) {
				t.Fatalf("Get = %v, want CALL_FAILED", err)
			}
			got, ok := svcres.RetryAfter(err)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("RetryAfter = %v, %v; want %v, %v", got, ok, c.want, c.wantOK)
			}
		})
	}
}
