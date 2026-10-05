package docstore

// exitConfig matches sysexits EX_CONFIG (78): the store was wired wrong.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65): the stored data is wrong.
const exitDataErr int = 65

// exitIOErr matches sysexits EX_IOERR (74): the filesystem failed.
const exitIOErr int = 74

// exitTempFail matches sysexits EX_TEMPFAIL (75): the database did not answer,
// and the same call may well succeed once it does.
const exitTempFail int = 75

// httpBadRequest is 400 Bad Request: the call itself is wrong — an empty key,
// a renaming update, an index that is not there or not unique, a key too long.
const httpBadRequest int = 400

// httpNotFound is 404 Not Found: no document, or no version, under what was
// asked for.
const httpNotFound int = 404

// httpConflict is 409 Conflict: the key, or a unique index's key, is taken.
const httpConflict int = 409

// httpInternalServerError is 500 Internal Server Error: the store or its data
// failed, not the call.
const httpInternalServerError int = 500

// httpServiceUnavailable is 503 Service Unavailable: the store is closed, or
// its database did not answer — the same call may succeed later.
const httpServiceUnavailable int = 503
