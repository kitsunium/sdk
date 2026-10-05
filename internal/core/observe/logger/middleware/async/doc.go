// Package async declares the codes and sentinels of the logger's asynchronous
// middleware, internal/service/observe/logger/middleware/async: range 0.3.17.*,
// allocated to that engine (ADR 0005 service/observe/logger/middleware/async
// block) and declared here since ADR 0160, so the engine declares none.
//
// Package async — declares the sentinels returned by the async
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package async
