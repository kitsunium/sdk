// Package secret — ranges 0.2.37.* (the port's verdicts) and 0.3.68.* (the
// engines' own refusals) — ADR 0096 and ADR 0142, declared here since ADR 0160.
//
// Package secret — declares the sentinel *errs.Error outcomes: the verdicts of
// the ports, and the refusals of the concrete stores, the keyring, the rotator
// and the subject keys in internal/service/security/secret (ADR 0160). Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private here carries a secret, and no field ever will: the
// whole domain exists so that a value can travel through a program without
// being written down by accident, and an error message is the most-written
// text a program produces. A secret's NAME is not secret and may travel as a
// log-only field, because an operator cannot act on "a secret is missing"
// without knowing which one.
package secret

// exitConfig matches sysexits EX_CONFIG (78). A missing secret, a malformed
// name and a write to a read-only store are all wiring faults: the same call
// is refused identically forever and the fix is in the deployment or at the
// call site, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may answer the next attempt, which is exactly what distinguishes
// StoreUnavailable from every other verdict in this block.
const exitTempFail int = 75

// httpUnavailable is 503: the store, not the request, is the problem.
const httpUnavailable int = 503
