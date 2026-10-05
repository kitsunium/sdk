package statemachine

// httpNotFound is 404: the entity addressed does not exist.
const httpNotFound int = 404

// httpConflict is 409: the entity's current state, or an existing entity,
// refuses the request.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up waiting, nothing is wrong with
// the request.
const httpUnavailable int = 503
