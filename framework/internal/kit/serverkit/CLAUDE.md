<!-- updated: 2026-10-05T00:00:00Z -->
# framework/internal/kit/serverkit — the HTTP engine and the frontends' file server

What `framework/kit/server` plugs into kit (`plug`): the HTTP engine an app
in the server profile serves on (the SDK's `net/server`: one group, one socket,
kit's bounds) and a frontend's file server (the SDK's `net/static`, a
single-page application under kit's CSP). A daemon or a CLI links and
initialises neither.

## Contents

| File | Holds |
|---|---|
| `serverkit.go` | `Enable`, which sets `plug.NewHTTPServer` and `plug.NewStaticFiles`; `httpServer` over `server.Server` |
| `serverkit_compliance.go` | `var _ plug.HTTPServer = httpServer{}` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/kit/serverkit/ && GOWORK=off go test ./internal/kit/ -run 'Listener|Static|Frontend'
```
