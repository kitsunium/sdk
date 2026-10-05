package validation

// exitConfig matches sysexits EX_CONFIG (78). A constraint refused at
// construction is a permanent configuration fault: the same arguments will be
// refused forever, and the fix is a code change at the call site, never a retry.
// A refused tag is one too: the same type will be refused identically
// forever, and the fix is a source change.
const exitConfig int = 78

// httpUnprocessable is RFC 9110 422 — the request was syntactically fine and
// semantically wrong, which is exactly what a failed validation is. The errs
// default (500) would tell an HTTP edge that the SERVER is at fault.
const httpUnprocessable int = 422
