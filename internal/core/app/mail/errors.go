package mail

// exitDataErr matches sysexits EX_DATAERR (65). The caller handed the domain a
// message it cannot act on; the program is fine, the argument is not.
const exitDataErr int = 65

// exitNoPerm matches sysexits EX_NOPERM (77). A header-injection refusal is a
// security verdict rather than a formatting accident, and a supervisor reading
// exit statuses deserves to tell the two apart — as are a refused downgrade
// and a refused cleartext credential, which belong here and not among the I/O
// accidents.
const exitNoPerm int = 77

// exitUnavailable matches sysexits EX_UNAVAILABLE (69). The message names a
// capability this domain does not offer; retrying changes nothing.
const exitUnavailable int = 69

// exitConfig matches sysexits EX_CONFIG (78): a permanent wiring fault. The
// same call will be refused identically forever and the fix is an edit at the
// call site.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75): the far side was not
// reachable or not willing right now. Retrying is meaningful.
const exitTempFail int = 75

// exitNoHost matches sysexits EX_NOHOST (68): the named host did not answer.
const exitNoHost int = 68
