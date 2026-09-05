// Package main — the module-proxy client behind the freshness probe.
package main

import "net/http"

// probe fetches the version list from a module proxy. The proxy and client are
// fields rather than globals so a test can point it at an httptest server and
// never touch the network.
type probe struct {
	// proxy is the base URL of the module proxy.
	proxy string
	// client bounds the request; nil falls back to a timeout-bounded default.
	client *http.Client
}
