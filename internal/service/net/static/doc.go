// Package static — what NewHandler refuses in a Config.
//
// Package static — opening a name of the tree, and telling a name that names
// nothing from a tree that failed.
//
// Package static — the lookup failures a Unix kernel reports for a name.
//
// Package static — the lookup failures Windows reports for a name.
//
// Package static — one file's response: its caching, its type, its body.
//
// Package static serves a tree of files from an io/fs.FS over HTTP — what
// http.FileServerFS does, and what it does not (ADR 0130).
//
// A request path is cleaned from the root before anything is looked up, so a
// ".." never climbs above the tree, whatever the file system does with names.
// A directory is never listed: it is served by its index.html, or it is 404.
// An optional single-page-application fallback serves the root's index.html
// for a path with no extension that names nothing, so client-side routes
// survive a reload while a missing script is still a 404 and never a page of
// HTML. Every response — a file, a redirect, a 404, a 405, a 500 — carries a
// Content-Security-Policy, X-Content-Type-Options: nosniff and a
// Referrer-Policy. A file the caller marks content-hashed is cached for a year
// and never revalidated; everything else is revalidated before each use.
//
// Only GET and HEAD are answered, and HEAD answers exactly what GET would,
// without the body. Every status goes through the ResponseWriter the handler
// was given, so a wrapping handler that records the status sees every 5xx: a
// file the tree holds but cannot open, stat or seek.
package static
