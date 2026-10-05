// Package logger — range 0.3.1.*, the logger engine's (ADR 0005
// service/observe/logger block), declared here since ADR 0160: the engine,
// internal/service/observe/logger, returns these and declares none.
//
// Package logger — declares the Encoder port — the format-side
// boundary that mirrors Sink (transport-side). Both ports live in core/
// per hexagonal architecture conventions; concrete adapters live under
// internal/service/observe/logger/encoder/. See ADR 0005.
//
// Package logger — declares the sentinels the logger engine
// (internal/service/observe/logger) returns from its constructors and Handle
// methods. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package logger

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a log-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74
