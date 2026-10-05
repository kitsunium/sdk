// Package lifecycle — ranges 0.2.19.* (ADR 0050 core/app/lifecycle block) and
// 0.3.49.* (ADR 0050 service/app/lifecycle block, declared here since ADR 0160).
//
// Package lifecycle — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the engine's and the supervisor's
// verdicts on a run. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
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
