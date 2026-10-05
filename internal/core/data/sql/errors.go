package sql

// exitConfig matches sysexits EX_CONFIG (78). A refused dialect and a refused
// migration are permanent wiring faults: the same call will be refused
// forever, and the fix is a code change, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75): a timing or availability
// outcome, where the same call on a less loaded system may well succeed.
const exitTempFail int = 75

// httpUnavailable is the wire status for "the database did not answer": 503
// Service Unavailable rather than the default 500, because the caller's request
// was never the problem, and a load balancer reads the difference.
const httpUnavailable int = 503
