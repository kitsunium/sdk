package profiling

// httpBadRequest is 400: the window asked for is the caller's to fix.
const httpBadRequest int = 400

// httpConflict is 409: the profiler is taken; asking again later works.
const httpConflict int = 409

// httpUnavailable is 503: the caller gave up; nothing is wrong with the ask.
const httpUnavailable int = 503
