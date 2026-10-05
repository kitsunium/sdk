// Package cli — ranges 0.2.32.* (ADR 0065 core/app/cli block) and 0.3.62.*
// (ADR 0065 service/app/cli block, declared here since ADR 0160).
//
// Package cli — declares the sentinel *errs.Error outcomes of the domain: the
// refused declarations, and the engine's verdicts on a command line. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package cli

// exitConfig matches sysexits EX_CONFIG (78). Every declaration sentinel
// carries it: the same tree will be refused identically forever, the operator
// did nothing wrong, and the fix is a code change in main. That is not the
// same event as a mistyped command line, which carries exitUsage.
const exitConfig int = 78

// exitUsage matches sysexits EX_USAGE (64): the command line was wrong. It is
// deliberately a different status from the exitConfig a refused DECLARATION
// carries — one says the operator mistyped, the other says main is wired
// wrong, and a supervisor that restarts on one and not the other needs to be
// able to tell them apart.
const exitUsage int = 64

// exitIOErr matches sysexits EX_IOERR (74): an error occurred while doing I/O —
// here, on the stream the help is written to.
const exitIOErr int = 74
