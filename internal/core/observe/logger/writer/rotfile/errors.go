package rotfile

// exitIOErr matches sysexits EX_IOERR — used by the I/O sentinels so CLI
// consumers treat a rotating-file failure as an I/O problem rather than a
// generic internal software error (70).
const exitIOErr int = 74

// exitConfigErr matches sysexits EX_CONFIG — used by the config-decode sentinel
// so a malformed config map is classified as a configuration problem, not I/O.
const exitConfigErr int = 78
