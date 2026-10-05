package journald

// exitIOErr matches sysexits EX_IOERR — a journald transport failure is an I/O
// problem, not a generic internal software error (70).
const exitIOErr int = 74
