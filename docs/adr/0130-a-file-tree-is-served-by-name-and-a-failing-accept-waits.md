# ADR 0130 — a file tree is served by name and never listed, and a failing accept waits

- **Status**: Accepted
- **Date**: 2026-09-26
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0029](0029-sdk-net-domain.md) (the inbound engine's accept loop)
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape while v0), [ADR 0095](0095-windows-runs-every-test-and-gates.md) (the Windows lane), [ADR 0103](0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md) (the published backoff curve)

## Context

A downstream framework serves two single-page applications from an
`embed.FS` — a product's frontend and its own developer console — and wrote the
serving twice: once on `http.FileServerFS` with headers set around it, once by
hand. Measured on go1.27.1, `http.FileServerFS` over an `fstest.MapFS` holding
`docs/a.txt`:

| request | `http.FileServerFS` |
|---|---|
| `GET /docs/` | 200, an HTML listing of `a.txt` |
| `POST /app.js` | 200, the file |
| any | no Content-Security-Policy, no `X-Content-Type-Options`, no Referrer-Policy, no Cache-Control |
| `GET /aaaa…` (a 300-byte component) over `os.DirFS` | **500** (`ENAMETOOLONG`) |
| `GET /a%00b` over `os.DirFS` | **500** |

The last two are failures any client can produce at will, in the signal an
operator alerts on and a framework turns into a failed span. The console's
hand-written copy fell back to its shell for every unreadable name, so a
missing script was answered with HTML. And the standard library lets a host's
`/etc/mime.types` or Windows registry override its own MIME table — Go itself
special-cases the registry's `.js = text/plain` (golang/go#32350) — which under
`nosniff` is a script the browser refuses to run.

Separately, a review of the same framework measured the engine's accept loop on
Linux: an `Accept` that fails for any reason but a closed listener — the
process out of descriptors, with a connection still queued — was retried at
once, **1.16 s of CPU per second of wall time**, one core per shard. net/http's
`Serve` backs off: 5 ms, doubling to 1 s, reset by a successful accept.

## Decision

### D1 — `server/static`: a handler over an `fs.FS`

A new service package, `internal/service/net/static`, published as
`pkg/v1/server/static`: `New(fsys, Config) (*Handler, error)`.

- **The name is cleaned from the root** — `path.Clean("/" + path)` made
  relative — before any lookup, so it is a valid `io/fs` name by construction
  and no spelling of `..` reaches the file system, whatever that file system
  would do with one.
- **A directory is its index.html, or a 404.** Never a listing, never the
  single-page shell. A directory named without its trailing slash is first
  redirected to it, so the page's relative links resolve inside it; the
  `Location` is relative (it stays right behind `http.StripPrefix`, where the
  handler never sees the prefix) and starts with `./` (no directory name makes
  it read as a host or a scheme).
- **The fallback serves routes only.** With `SinglePageApp`, a path with no
  extension that names nothing gets the root's index.html with 200; a path with
  one stays a 404.
- **Every response carries the headers** — a file, a redirect, a 404, a 405, a
  500: Content-Security-Policy (the caller's, else
  `default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'`),
  `X-Content-Type-Options: nosniff`, Referrer-Policy (the caller's, else
  `same-origin`). The handler SETS them: the configuration is the policy.
  Cache-Control is `public, max-age=31536000, immutable` for a file the
  caller's `Immutable(name)` marks content-hashed, `no-cache` for every other
  response.
- **Only GET and HEAD**, anything else 405 with `Allow`. HEAD answers what GET
  would, without the body.
- **The seven web types are pinned** — HTML, JavaScript, CSS, JSON and source
  maps, WebAssembly, SVG, the web manifest — independent of the host's tables;
  every other type is the host's, then the bytes'.
- **A 404 is the name's, a 500 is the tree's.** A lookup the file system refuses
  because of the NAME — `fs.ErrNotExist`, `fs.ErrInvalid`, `ENOTDIR` and
  `ENAMETOOLONG` on Unix, `ERROR_INVALID_NAME` / `ERROR_BAD_PATHNAME` /
  `ERROR_FILENAME_EXCED_RANGE` / `ERROR_DIRECTORY` on Windows — is a 404. Any
  other failure — a permission refused, a descriptor table full, a storage that
  failed — is a 500, and so is an entry that opened and cannot be stat'ed. Every
  status goes through the `ResponseWriter` given, so a wrapping recorder sees
  every 5xx. A failure after the status cannot change it; the body stops short
  of its declared length.
- **A file that cannot seek is streamed whole**, its first 512 bytes read before
  the status so an unreadable one is still a 500.
- **`New` refuses** a nil tree, a header value holding a control character, and
  a Referrer-Policy that is not a list of the eight tokens the specification
  defines — a browser ignores an unknown token and falls back to a default that
  sends the origin everywhere. One core sentinel, `STATIC_MISCONFIGURED`
  (`0.2.11.34`), with an `option` field; per ADR 0029 the net service layer
  declares no codes. `New` reads nothing from the tree.

### D2 — a failing accept waits, on the engine's clock

`acceptLoop` retries an `Accept` that failed — temporary or not — only after a
wait: 5 ms, doubling, held at 1 s, started over by the next accepted
connection. The curve is `service/resilience`'s published `BackoffValue`
(ADR 0103) with those three values, not a hand-written copy. The wait is on the
engine's clock, a new `Server.clk` (`clock.System`), and a listener closed
during it ends it at once: `boundListener` carries a channel its `Close`
closes, which the wait selects on, because a loop waiting is not blocked in
`Accept` and closing the socket alone would not reach it. Every wait is counted
in a new `State.AcceptBackoffs`, beside `RejectedConns` and
`OversizedPackets`.

## Consequences

- The framework's two copies become two `static.New` calls; its console keeps
  its own stricter policy through `Config`.
- A client can no longer produce a 5xx by the name it asks for; an operator
  alerting on 5xx sees the tree's failures and only those.
- A process out of descriptors costs a loop at most one `Accept` per second per
  listener instead of a core, and says so in `State`.

## Breaking changes

`State` (the `pkg/v1/server.State` alias of `corenet.StateValue`) gains the
`AcceptBackoffs` field — ADR 0040's v0 licence, said out loud: a composite
literal of `State` written without field names stops compiling. None exists in
this repository, and `State` is a value the engine RETURNS. `server/static` is
new.

## Alternatives considered

- **Wrap `http.FileServerFS`.** Keeps the listing and the name-caused 500s, and
  every correction becomes a response rewrite after the fact.
- **Every lookup failure a 404.** Hides a remote tree that stopped answering, or
  a deployment whose files the process cannot read, behind a status a CDN may
  cache.
- **Keep an outer handler's security headers when present.** Makes the headers a
  race between two layers; the handler's `Config` is where a caller says what it
  wants.
- **A public `WithClock` on the engine.** The accept backoff is the only wait on
  it today — the drain still polls the wall clock — so the option would move one
  wait of several; the internal field lets the suite drive it.

## Deferred

- Pre-compressed assets (`.gz` / `.br` beside a file) and an ETag for an
  `embed.FS`, whose files carry no modification time.
- An `OnError` hook handing the cause of a 500 to the caller's log.
- The drain's poll on the engine's clock, and with it a public clock option.

## Verification

- `internal/service/net/static/static_external_test.go` — every resolution,
  `TestNoRequestClimbsAboveTheRoot` against a file system that `path.Join`s names
  (which climbs), the fallback, the headers on every status, 405, the caching
  split and what the predicate is asked, every refusal of `New`.
- `lookup_external_test.go` — `TestAFailingTreeIsA500AWrapperSees` through a
  status recorder; `TestNamesTheOperatingSystemRefusesAre404s` on a real
  `os.DirFS`, run by every lane including Windows.
- `serve_external_test.go` — HEAD against GET on every kind of answer, ranges,
  `TestPinnedTypesDoNotDependOnTheHost` with the process's MIME table made
  hostile, a tree that cannot seek, a read that fails before and after the
  status.
- `internal/service/net/server/accept_backoff_internal_test.go` — each wait to
  the nanosecond on a manual clock, no `Accept` while it stands still, the curve
  started over, and `Shutdown` during a wait returning while the clock never
  moves.

## References

- [`http.FileServerFS`](https://pkg.go.dev/net/http#FileServerFS), [`http.ServeContent`](https://pkg.go.dev/net/http#ServeContent), [`io/fs.ValidPath`](https://pkg.go.dev/io/fs#ValidPath)
- [Referrer Policy](https://www.w3.org/TR/referrer-policy/) — the eight tokens and the list form.
- [golang/go#32350](https://github.com/golang/go/issues/32350) — the registry's `.js` type.
- `net/http.(*Server).Serve` — the accept backoff it applies.
