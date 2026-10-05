package file

// exitIOErr matches sysexits EX_IOERR — used by WriteFailed and SyncFailed
// to let CLI consumers treat a file-write failure as an I/O problem rather
// than a generic internal software error (70).
const exitIOErr int = 74
