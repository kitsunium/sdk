// Package net — the outbound authorisation port.
package net

// Policy authorises an outbound request before it leaves the process.
//
// The contract that makes this worth a port: implementations are evaluated
// inside the RoundTripper, underneath every call site. A caller cannot construct
// a request that skips the check, so "this client is read-only" becomes a
// property of the code rather than a convention the next contributor must
// remember. Allow returns nil to permit the request and a typed error to refuse
// it; the refusal is reported as RequestDenied without echoing the path, so a
// denial cannot leak the private API surface through a log line.
//
// Implementations MUST be safe for concurrent use.
//
// IFACE-PLUGIN: concrete policies live in internal/service/net/client; their
// types stay unexported behind their constructors.
type Policy interface {
	Allow(req RequestValue) error
}

// PolicyFunc adapts a plain function to the Policy interface.
type PolicyFunc func(req RequestValue) error

// Allow implements Policy by calling f.
func (f PolicyFunc) Allow(req RequestValue) error {
	//: the function IS the policy — there is no state to consult.
	return f(req)
}
