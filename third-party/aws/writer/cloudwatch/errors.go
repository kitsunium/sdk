package cloudwatch

// exitIOErr matches sysexits EX_IOERR — a remote delivery failure is an I/O
// problem rather than a generic internal software error (70).
const exitIOErr int = 74
