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
