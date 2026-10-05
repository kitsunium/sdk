package transform

// exitDataErr matches sysexits EX_DATAERR — a malformed or undecompressable
// frame is a data problem, not a generic internal software error (70).
const exitDataErr int = 65
