package syslog

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed to let CLI
// consumers treat a syslog-write failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74
