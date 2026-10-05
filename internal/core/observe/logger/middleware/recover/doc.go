// Package recover declares the codes and sentinels of the logger's
// panic-recovering middleware,
// internal/service/observe/logger/middleware/recover: range 0.3.21.*, allocated
// to that engine (ADR 0005 service/observe/logger/middleware/recover block) and
// declared here since ADR 0160, so the engine declares none.
//
// Package recover — declares the sentinels returned by the
// recover Sink. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package recover
