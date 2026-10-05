// Package vfs — range 0.2.25.* (ADR 0056 core/data/vfs block), and the
// concrete filesystem's 0.3.55.* (ADR 0056 service/data/vfs block, declared
// here since ADR 0160).
//
// Package vfs — declares the sentinel *errs.Error port outcomes, and the two
// a concrete filesystem in internal/service/data/vfs can produce and an
// abstract one cannot (ADR 0160: every code is declared in the core, at the
// service's path). Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// No Public string here names a path. A Public is read by third parties, and a
// path is the one piece of caller data a filesystem error is guaranteed to
// hold; it travels as a log-only field instead, reachable through errs.FieldsOf.
package vfs

// exitDataErr matches sysexits EX_DATAERR (65). The caller handed the domain
// something it cannot act on; the program is fine, the argument is not.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77). A confinement refusal is a
// security verdict, not an I/O accident, and deserves to be distinguishable
// from one in a supervisor's exit status.
const exitNoPerm int = 77

// exitConfig matches sysexits EX_CONFIG (78). A refused mode is a permanent
// wiring fault: the same call will be refused identically forever, and the fix
// is an edit at the call site.
const exitConfig int = 78

// exitIOErr matches sysexits EX_IOERR (74). The filesystem itself failed.
const exitIOErr int = 74
