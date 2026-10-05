package selfupdate

// exitUsage matches sysexits EX_USAGE (64). A candidate install with no tag is
// a command that was written wrong; nothing about the system is broken.
const exitUsage int = 64

// exitCantCreate matches sysexits EX_CANTCREAT (73). Staging, replacing and an
// authorised escalation all fail because an output file could not be created
// or moved, which is exactly what that status is for.
const exitCantCreate int = 73
