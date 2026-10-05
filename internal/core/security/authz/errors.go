package authz

// httpForbidden is RFC 9110 403 — the request was understood and the server
// refuses to authorize it. It is the status EVERY verdict of the port carries,
// including the two that are really evaluation faults, because the alternative
// (500 for an unevaluable rule) tells a client to retry a request that will be
// refused identically forever, and tells an attacker which of their inputs the
// policy could not parse.
//
// A framework that would rather answer 404 to hide the resource's existence
// overrides it at the edge. The SDK picks the honest default and does not
// decide the response shape; see internal/core/security/authz/CLAUDE.md §The frontier.
const httpForbidden int = 403

// exitConfig matches sysexits EX_CONFIG (78). A policy that was assembled
// wrong is a permanent wiring fault: the same composition will refuse every
// request forever, and the fix is a code change, never a retry. The engine's
// three construction refusals carry it for the same reason.
const exitConfig int = 78
