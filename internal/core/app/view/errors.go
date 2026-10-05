package view

// exitConfig matches sysexits EX_CONFIG (78). A Renderer refused at
// construction is a permanent fault: the same tree will be refused forever, and
// the fix is an edit, never a retry.
const exitConfig int = 78

// exitSoftware matches sysexits EX_SOFTWARE (70) — the errs default, restated
// where a sentinel wants it explicitly.
const exitSoftware int = 70

// httpInternal is 500. Every render verdict in this domain is a server fault:
// the caller asked for a page, and which page failed is not something the
// browser gets to learn.
const httpInternal int = 500
