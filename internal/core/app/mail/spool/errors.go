package spool

// exitConfig matches sysexits EX_CONFIG (78): the spool was wired wrong.
const exitConfig int = 78

// httpBadRequest is RFC 9110 400: the mail or the identifier the caller handed
// over is one no spool can keep, and resending it unchanged changes nothing.
const httpBadRequest int = 400

// httpUnavailable is RFC 9110 503: the spool is closed. Nothing is wrong with
// the request; this process will not take it.
const httpUnavailable int = 503
