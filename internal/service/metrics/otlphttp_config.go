// Package metrics — the OTLP/HTTP exporter's configuration.
package metrics

import (
	"net/http"
	"time"
)

// OTLPHTTPConfig configures an OTLP/HTTP exporter. Every field has a resolved
// meaning when left unset except Endpoint, which has none that could be right.
type OTLPHTTPConfig struct {
	// Endpoint is the FULL URL the payload is POSTed to, used exactly as
	// given — no path is appended, no scheme is guessed.
	//
	// That mirrors the specification's own split: the per-signal endpoint
	// variable is documented as a full URL used without modification, and it
	// exists precisely because joining a base with a path is where collectors
	// and SDKs disagree. A caller writes
	// "http://collector:4318" + OTLPMetricsPath, where a reviewer can see the
	// whole address in one string.
	//
	// It is REFUSED at construction when it is not an absolute http(s) URL
	// with a host and a non-root path (OTLPEndpointInvalid): a bare host would
	// POST to "/" and collect 404s forever, which is the silent failure this
	// whole domain exists to avoid.
	Endpoint string

	// Client is the http.Client the POST rides on. When nil, a client bounded
	// by Timeout and refusing every redirect is built.
	//
	// A supplied client is used AS-IS, including its own timeout and redirect
	// policy — it is the seam a caller uses to install a proxy, mTLS identity
	// or an SSRF allowlist, and second-guessing it here would defeat that.
	Client *http.Client

	// Headers are set on every request before Content-Type, so a hosted
	// collector's Authorization or tenant header can ride along. Their values
	// are treated as secrets: they are never echoed into an error or a field.
	Headers map[string]string

	// Timeout bounds one export round trip, request and response body
	// together. Non-positive CLAMPS to DefaultOTLPTimeout; there is no
	// "no timeout" setting, because a stalled collector must never wedge the
	// goroutine that is scraping (ADR 0031).
	//
	// It is ignored when Client is non-nil — a caller who brought their own
	// client brought their own deadline.
	Timeout time.Duration

	// MaxResponseBytes caps the response body read. Non-positive CLAMPS to
	// DefaultOTLPMaxResponseBytes.
	MaxResponseBytes int64
}
