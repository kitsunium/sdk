// Package multi declares the codes and sentinels of the logger's fan-out
// middleware, internal/service/observe/logger/middleware/multi: range
// 0.3.16.*, allocated to that engine (ADR 0005 service/observe/logger/middleware/multi
// block) and declared here since ADR 0160, so the engine declares none.
//
// Package multi — declares the sentinel the fan-out Sink returns. Its name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package multi
