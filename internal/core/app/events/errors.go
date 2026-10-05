// Package events — ranges 0.2.22.* (ADR 0053 core/app/events block) and
// 0.3.52.* (ADR 0053 service/app/events block, declared here since ADR 0160).
//
// Package events — declares the sentinel *errs.Error outcomes of the domain:
// the registration refusals, the control sentinel, and the bus's verdicts on
// a dispatch. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package events

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Subscribe will be refused forever, and the
// fix is a code change at the call site, never a retry. A listener that halts
// without the authority to is one too: the same dispatch will refuse it
// identically forever, and the fix is a field at the registration site.
const exitConfig int = 78
