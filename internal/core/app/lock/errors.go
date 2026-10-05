package lock

// exitConfig matches sysexits EX_CONFIG (78). A locker refused at construction
// is a permanent configuration fault: the same arguments will be refused
// forever, and the fix is a code change at the call site, never a retry. An
// unsafe lock directory is one too — a deployment fault: the same path will be
// refused until someone changes it, and no retry helps.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65). A rejected name is malformed
// input to the locker, not a broken locker and not a broken configuration; a
// corrupt fence ledger is bad data on disk, for the same reason.
const exitDataErr int = 65
