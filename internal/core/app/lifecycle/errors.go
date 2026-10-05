package lifecycle

// exitConfig matches sysexits EX_CONFIG (78). A refused registration — and a
// supervisor wired wrong — is a permanent wiring fault: the same call will be
// refused forever, and the fix is a code change at the call site, never a
// retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A budget that expired is a
// timing outcome, not a permanent fault: the same shutdown attempted again on
// a less loaded machine may well complete.
const exitTempFail int = 75
