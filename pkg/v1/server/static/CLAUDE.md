<!-- updated: 2026-09-26T00:00:00Z -->
# pkg/v1/server/static/

## Purpose

Public facade for serving a file tree over HTTP (ADR 0130): a single-page
application, a documentation site, an `embed.FS`. Aliases onto
`internal/service/net/static` and the core sentinel, plus the forwarding `New`.
No logic lives here.

A subpackage of `pkg/v1/server` for the reason `sse` and `websocket` are: it is
an `http.Handler`, usable in any `net/http` chain, and `static.New` /
`static.Config` read the way the job is discussed.

## Surface

| Symbol | Role |
|---|---|
| `New(fsys, cfg)` | a `*Handler` over the tree, or `Misconfigured`; reads nothing from `fsys` |
| `Handler` | the `http.Handler`; the zero value answers 500 with the default headers |
| `Config` | `ContentSecurityPolicy`, `ReferrerPolicy`, `SinglePageApp`, `Immutable` — the zero value is working and strict |
| `DefaultContentSecurityPolicy`, `DefaultReferrerPolicy` | what an empty field sends |
| `ImmutableCacheControl`, `RevalidateCacheControl` | the two Cache-Control values a response can carry |
| `Misconfigured` | the core sentinel `STATIC_MISCONFIGURED` (`0.2.11.34`); the `option` field names what |

## Why-this-shape

- **One constructor returning an error**, because three configurations cannot
  be served safely and must be refused where they are written: no tree, a header
  value holding a control character, a Referrer-Policy token no browser knows.
- **`Config` has no switch for the security headers.** A policy is always sent;
  the most permissive one a caller wants is still a decision written down.
- **The sentinel is the core's own value, not a copy** —
  `TestMisconfiguredIsTheEnginesSentinel`.

## Do NOT

- Add logic here; it belongs in `internal/service/net/static`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/server/static:static_test
cd pkg && GOWORK=off go test -race ./v1/server/static/
```

## Reference

- ADR 0130; `internal/service/net/static/CLAUDE.md`
