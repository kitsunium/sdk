// Package health — ranges 0.2.29.* (ADR 0060 core/app/health block) and
// 0.3.59.* (ADR 0060 service/app/health block, declared here since ADR 0160).
//
// Package health — declares the sentinel *errs.Error outcomes of the domain:
// the registration refusals, and the engine's verdicts on a probe and on the
// loopback Ask. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// Every Public of an engine verdict is written to be READ BY A STRANGER. A
// probe endpoint is routinely exposed more widely than whoever added it
// expected — a mesh sidecar, a load balancer health page, an uptime checker —
// so these strings name a condition and never a cause, a host, a port or a
// query.
package health

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Add will be refused forever, and the fix is
// a code change at the call site, never a retry.
const exitConfig int = 78

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). A failing check is a
// runtime condition about something the process depends on, not a defect in
// the process's own configuration.
const exitUnavailable int = 69
