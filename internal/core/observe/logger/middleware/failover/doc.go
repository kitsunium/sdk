// Package failover declares the codes and sentinels of the logger's failover
// middleware, internal/service/observe/logger/middleware/failover: range
// 0.3.19.*, allocated to that engine (ADR 0005
// service/observe/logger/middleware/failover block) and declared here since ADR
// 0160, so the engine declares none.
//
// Package failover — declares the sentinels returned by the
// failover Sink. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package failover
