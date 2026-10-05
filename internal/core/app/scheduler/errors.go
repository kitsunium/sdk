package scheduler

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent configuration fault: the same Add will be refused forever, and the
// fix is a code change at the call site, never a retry. A refused schedule is
// one too: retrying the same expression will be refused identically, and the
// fix is an edit.
const exitConfig int = 78
