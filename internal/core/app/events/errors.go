package events

// exitConfig matches sysexits EX_CONFIG (78). A refused registration is a
// permanent wiring fault: the same Subscribe will be refused forever, and the
// fix is a code change at the call site, never a retry. A listener that halts
// without the authority to is one too: the same dispatch will refuse it
// identically forever, and the fix is a field at the registration site.
const exitConfig int = 78
