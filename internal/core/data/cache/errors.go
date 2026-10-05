// Package cache — declares the sentinel *errs.Error port outcomes, and the
// two the chained store in internal/service/data/cache emits (ADR 0160: every
// code is declared in the core, at the service's path). Each var's name equals
// its errs.Define Reason in SCREAMING_SNAKE form.
package cache

// exitConfig matches sysexits EX_CONFIG (78). A store refused at construction
// is a permanent configuration fault: the same arguments will be refused
// forever, and the fix is a code change at the call site, never a retry.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65). A rejected entry is malformed
// input to the store, not a broken store and not a broken configuration.
const exitDataErr int = 65
