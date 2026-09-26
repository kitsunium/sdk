<!-- updated: 2026-09-26T00:00:00Z -->
# internal/service/net/static/

## Purpose

Serves a tree of files from an `io/fs.FS` over HTTP (ADR 0130). Like
`http.FileServerFS` it cleans the request path from the root, so no `..` climbs
above it; unlike it — measured on go1.27.1 — it never lists a directory, answers
only GET and HEAD, sends the security headers on every response, and answers a
name the file system refuses with 404 where `FileServerFS` answers a 300-byte
component or a `%00` with a 500. It adds the single-page fallback, which never
answers a missing script with HTML. Public facade: `pkg/v1/server/static`.

Stdlib only. Written against `net/http`'s own interfaces, so it works in any
`http.Handler` chain — under `http.StripPrefix`, behind the SDK engine's
`Group.HandleHTTP`, behind a framework's status recorder.

## Contents

| File | Role |
|---|---|
| `static.go` | package doc, the four exported header constants, `Config`, `Handler`, `NewHandler` (the facade's `New`), `ServeHTTP`, `cleanName`, `serveName` / `serveDirectory` / `fallback` (the three ways a name is answered), the relative directory redirect, `notFound` / `failed` |
| `lookup.go` | `open` — one lookup, three outcomes (`present`, `absent`, `broken`); `nameRefused`, the 404-versus-500 split |
| `lookup_unix.go` / `lookup_windows.go` / `lookup_other.go` | `platformNameRefused` — the kernel's own refusals of a NAME: `ENOTDIR` and `ENAMETOOLONG` on Unix; `ERROR_INVALID_NAME`, `ERROR_BAD_PATHNAME`, `ERROR_FILENAME_EXCED_RANGE`, `ERROR_DIRECTORY` on Windows (as `syscall.Errno` literals — `syscall` exports none of them) |
| `serve.go` | `serveFile` (caching, pinned type, `http.ServeContent`), `stream` (a file that cannot seek), `pinnedType` |
| `config.go` | what `NewHandler` refuses: `headerValueSafe`, `referrerPolicyKnown`, `misconfigured` |

## Why-this-shape

- **The name is cleaned from the root before any lookup.** `path.Clean("/" +
  r.URL.Path)` made relative is a valid `io/fs` name by construction — no `..`,
  no empty element, no leading slash — so no spelling of a traversal reaches the
  file system, whatever the file system itself would do with one.
  `TestNoRequestClimbsAboveTheRoot` serves through a file system that joins
  names with `path.Join` (which climbs) and records every name it is asked for.
- **A directory is its index.html or a 404 — never a listing, never the SPA
  shell.** `http.FileServerFS` lists. A directory named without its trailing
  slash is redirected first, so the page's relative links resolve inside it; the
  `Location` is RELATIVE (right behind `http.StripPrefix`, where the handler never
  sees the prefix), escapes the name as one path segment (a directory called
  `v2?beta` or `a#b` is redirected to itself, not to a query or a fragment —
  found by review, `TestTheDirectoryRedirectEscapesTheName`) and starts with
  `./` (no directory name can make it read as a
  host or a scheme). The root, which `StripPrefix` can hand over as `""`, is
  served in place: there is no redirect this handler could aim.
- **The fallback serves only routes.** A path with no extension that names
  nothing gets the root's index.html with 200; one WITH an extension stays 404,
  so a script tag never receives HTML (the classic `Unexpected token '<'`).
- **404 is the name's, 500 is the tree's.** The client chooses every name it
  asks for, so every refusal of a NAME — nothing there, a character the file
  system cannot hold, a file used as a directory, a component past `NAME_MAX` —
  is a 404, or any client could make the server report failures at will.
  Everything else a lookup can say — a permission refused, a descriptor table
  full, a disk or a remote store that failed — is a 500, and so is an entry that
  opened and cannot be stat'ed. `TestNamesTheOperatingSystemRefusesAre404s` asks
  a real `os.DirFS` on each platform.
- **Every status goes through the `ResponseWriter` given.** No hijack, no
  `ResponseController` for the status, so a framework's recorder sees every
  5xx. A failure after the status is sent cannot change it: the body stops short
  of its declared length, which a client sees as a broken response.
- **The handler owns its headers.** CSP, `nosniff`, Referrer-Policy and
  Cache-Control are SET on every response, whatever an outer handler left: the
  configuration is the policy. `Cache-Control` is `no-cache` unless a
  content-hashed file is being served — a 404 under `assets/` is never cached
  for a year.
- **Web types are pinned.** `mime.TypeByExtension` lets `/etc/mime.types` or the
  Windows registry override the standard library's table, and under `nosniff`
  a script or stylesheet served as `text/plain` is refused by the browser. The
  seven extensions a page cannot run without are fixed in `pinnedType`; the rest
  come from the host's table, then from the bytes.
- **A tree whose files cannot seek is served whole.** An archive's files have no
  `Seek`, so `http.ServeContent` cannot take them; `stream` sends the whole file
  with its stat's length and no ranges, reading the first 512 bytes before the
  status so a file that cannot be read at all is still a 500.
- **`NewHandler` reads nothing.** A tree that appears after the handler is built — a
  dev build still running — is served once it is there.

## Error range

None of its own. `NewHandler`'s refusals are the core sentinel `STATIC_MISCONFIGURED`
(`0.2.11.34`), with an `option` field; per ADR 0029 the net service layer
declares no codes.

## Do NOT

- Pass a request path to the file system uncleaned.
- Answer a lookup error with 500 without asking whether it was the NAME.
- Replace the relative redirect with `http.Redirect`: it rewrites a relative
  target against the path the handler sees, which under `StripPrefix` has lost
  its prefix.
- Let the fallback serve a path that has an extension.
- Write to stdout (ADR 0030).

## Verification

```
bazel test --config=race //internal/service/net/static:static_test
cd internal/service && GOWORK=off go test -race ./net/static/
```

`TestPinnedTypesDoNotDependOnTheHost` changes the process's MIME table and puts
it back; it is the one test here that does not run in parallel.
`TestAnUnreadableFileIsTheTreesFailure` skips on Windows (a mode does not make a
file unreadable there) and as root (root reads a mode-000 file); the portable
`TestAFailingTreeIsA500AWrapperSees` pins the same verdict everywhere.

## Reference

- ADR 0130 — `docs/adr/0130-a-file-tree-is-served-by-name-and-a-failing-accept-waits.md`
- ADR 0029 (the net domain), ADR 0031 (zero values), ADR 0095 (Windows gates)
