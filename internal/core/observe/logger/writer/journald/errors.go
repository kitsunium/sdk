// Package journald declares the codes and sentinels of the journald log writer,
// internal/service/observe/logger/writer/journald: range 0.3.31.*, allocated to
// that engine (ADR 0015 service slot 0x1f) and declared here since ADR 0160, so
// the engine declares none.
//
// Package journald — declares the sentinels returned by the journald sink's
// constructor and Write path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. No record content or socket path is ever echoed into
// these errors.
package journald

// exitIOErr matches sysexits EX_IOERR — a journald transport failure is an I/O
// problem, not a generic internal software error (70).
const exitIOErr int = 74
