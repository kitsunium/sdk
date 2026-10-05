// Package scheduler — ranges 0.2.12.* (ADR 0041 core/app/scheduler block) and
// 0.3.43.* (ADR 0041 service/app/scheduler block, declared here since ADR 0160).
//
// Package scheduler — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the parser's refusals of a schedule.
// Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package scheduler

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent configuration fault: the same Add will be refused forever, and the
// fix is a code change at the call site, never a retry. A refused schedule is
// one too: retrying the same expression will be refused identically, and the
// fix is an edit.
const exitConfig int = 78
