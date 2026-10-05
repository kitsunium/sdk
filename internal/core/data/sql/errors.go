// Package sql — range 0.2.24.* (ADR 0055 core/data/sql block), and the
// transaction manager's, health probe's and migration runner's 0.3.54.* (ADR
// 0055 service/data/sql block, declared here since ADR 0160).
//
// Package sql — declares the sentinel *errs.Error port outcomes, and the run
// outcomes of the transaction manager, the health probe and the migration
// runner in internal/service/data/sql (ADR 0160: every code is declared in the
// core, at the service's path). Each var's name equals its errs.Define Reason
// in SCREAMING_SNAKE form.
//
// # Public never carries infrastructure
//
// Driver errors are the SDK's worst leak risk: a failed connection reports the
// host, the port, the user and sometimes the password; a failed statement
// reports the statement. Every Public below is a fixed literal that names the
// CLASS of failure and nothing else, and no call site in this domain ever puts
// a DSN or a SQL fragment into one. Where the detail matters it goes in a
// Private or a field, both of which are log-only; and the run outcomes the
// service emits travel beside the driver's own error under errors.Join —
// reachable from a log, never from errs.PublicOf (ADR 0055 §D9).
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
