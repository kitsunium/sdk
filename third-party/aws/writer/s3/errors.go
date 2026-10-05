package s3

// exitIOErr matches sysexits EX_IOERR — a remote upload failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74
