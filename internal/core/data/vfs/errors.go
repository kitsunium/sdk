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
