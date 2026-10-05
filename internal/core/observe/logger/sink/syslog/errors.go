// Package syslog declares the codes and sentinels of the logger's syslog sink,
// internal/service/observe/logger/sink/syslog: range 0.3.15.*, allocated to
// that engine (ADR 0005 service/observe/logger/sink/syslog block) and declared
// here since ADR 0160, so the engine declares none.
//
// Package syslog — declares the sentinels returned by the syslog
// Sink. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
package syslog

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a syslog-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74
