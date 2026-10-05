package nettransport

// exitIOErr matches sysexits EX_IOERR — a network transport failure is an I/O
// problem, not a generic internal software error (70).
const exitIOErr int = 74
