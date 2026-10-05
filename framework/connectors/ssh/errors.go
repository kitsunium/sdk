package ssh

// exitCantCreate matches sysexits EX_CANTCREAT (73). Every failure under
// EnrolmentFailed is an output file that could not be created or written,
// which is exactly what that status is for.
const exitCantCreate int = 73
