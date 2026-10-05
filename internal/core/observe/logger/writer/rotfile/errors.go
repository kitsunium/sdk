// Package rotfile declares the codes and sentinels of the rotating-file log
// writer, internal/service/observe/logger/writer/rotfile: range 0.3.27.*,
// allocated to that engine (ADR 0014 service slot 0x1b) and declared here since
// ADR 0160, so the engine declares none.
//
// Package rotfile — declares the sentinels returned by the rotating file
// sink's constructor and Write / rotate paths. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package rotfile

// exitIOErr matches sysexits EX_IOERR — used by the I/O sentinels so CLI
// consumers treat a rotating-file failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

// exitConfigErr matches sysexits EX_CONFIG — used by the config-decode sentinel
// so a malformed config map is classified as a configuration problem, not I/O.
const exitConfigErr int = 78
