// Package http2 serves HTTP/2 beside HTTP/1.1 on the SDK's loop engine (ADR 0220):
// HandleLoop adapts a Handler of package http1 to a loop group, the connection's
// first bytes choosing the version, so one chain of Routes, Bearer, Headers and
// Log serves both. h2c by prior knowledge, or h2 over TLS (ADR 0198); the
// Upgrade: h2c form is not served.
package http2
