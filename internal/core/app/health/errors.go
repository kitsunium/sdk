package health

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Add will be refused forever, and the fix is
// a code change at the call site, never a retry.
const exitConfig int = 78

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). A failing check is a
// runtime condition about something the process depends on, not a defect in
// the process's own configuration.
const exitUnavailable int = 69
