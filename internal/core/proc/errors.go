package proc

// sysexits exit-code mapping (see sysexits.h) — chosen so a supervisor that
// surfaces err.ExitCode() reports a meaningful status rather than a generic 70.
const (
	exitUsage       int = 64 // EX_USAGE — caller passed an invalid argument.
	exitDataErr     int = 65 // EX_DATAERR — input data was malformed.
	exitNoUser      int = 67 // EX_NOUSER — a named user/group did not resolve.
	exitUnavailable int = 69 // EX_UNAVAILABLE — a required facility is absent.
	exitOSErr       int = 71 // EX_OSERR — an OS-level operation failed.
	exitIOErr       int = 74 // EX_IOERR — an I/O error occurred.
	exitNoPerm      int = 77 // EX_NOPERM — a permission/credential check failed.
)
