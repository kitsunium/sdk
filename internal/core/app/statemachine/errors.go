// Package statemachine — ranges 0.2.56.* (ADR 0120 core/app/statemachine
// block) and 0.3.88.* (ADR 0120 service/app/statemachine block, declared here
// since ADR 0160).
//
// Package statemachine — declares the sentinel *errs.Error outcomes of the
// domain: the contract's one refusal, and the engine's outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public text names a key, an event or a state: those travel as log-only
// fields, because a key is often an identifier a caller would rather not see
// echoed, and the fields are where an operator looks anyway.
package statemachine

// httpNotFound is 404: the entity addressed does not exist.
const httpNotFound int = 404

// httpConflict is 409: the entity's current state, or an existing entity,
// refuses the request.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up waiting, nothing is wrong with
// the request.
const httpUnavailable int = 503
