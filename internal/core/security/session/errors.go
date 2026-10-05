// Package session — ranges 0.2.14.* (the port's verdicts) and 0.3.46.* (the
// engines' own refusals) — ADR 0045, declared here since ADR 0160.
//
// Package session — declares the sentinel *errs.Error outcomes: the verdicts
// of the port, and the refusals specific to the engines in
// internal/service/security/session (ADR 0160). Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public string here is written on the assumption that a third party
// reads it: none of them contains an identifier, a subject, a data key, a
// directory path, or a count. The identifier in particular is a bearer secret,
// so it never appears in a Public, in a Private, or in a Field.
package session

// exitConfig matches sysexits EX_CONFIG (78). A refused store configuration —
// and a refused location or purpose — is permanent: the same call will be
// refused identically forever and the fix is an edit, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may be reachable on the next attempt, and a store-wide lock that
// could not be taken may be free on it, which is what distinguishes both from
// a configuration fault.
const exitTempFail int = 75

// httpUnauthorized is 401. Every verdict that means "this request carries no
// usable session" maps to it, so a framework can route the whole family with
// one errs.HTTPStatusOf call instead of a switch it has to keep in sync.
const httpUnauthorized int = 401

// httpUnavailable is 503 — the store, not the request, is the problem.
const httpUnavailable int = 503

// exitDataErr matches sysexits EX_DATAERR (65). A record that does not decode
// is bad data, not a bad program and not a transient fault.
const exitDataErr int = 65

// httpPayloadTooLarge is 413 — the caller asked to store more than the store
// will hold.
const httpPayloadTooLarge int = 413
