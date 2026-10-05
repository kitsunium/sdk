// Package console declares the codes and sentinels of the logger's console
// sink, internal/service/observe/logger/sink/console: range 0.3.13.*, allocated
// to that engine (ADR 0005 service/observe/logger/sink/console block) and
// declared here since ADR 0160, so the engine declares none.
//
// Package console — declares the sentinels the console sink's constructor and
// Write method return. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
package console

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a console-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74
