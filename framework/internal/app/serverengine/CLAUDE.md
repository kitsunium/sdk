<!-- updated: 2026-10-05T00:00:00Z -->
# framework/internal/app/serverengine — the HTTP engine, the frontends' file server, the process sampler

What `framework/app/server` plugs into kit (`plug`): the HTTP engine an app
in the server profile serves on (the SDK's `net/server`: one group, one socket,
kit's bounds), a frontend's file server (the SDK's `net/static`, a
single-page application under kit's CSP), and the process sampler of the
runtime graph a serving app answers (the SDK's `proc/process.Self`, and through
it `runtime/metrics` and the spawn engine — ADR 0172). A daemon or a CLI links
and initialises none of them.

## Contents

| File | Holds |
|---|---|
| `serverengine.go` | `Enable`, which sets `plug.NewHTTPServer`, `plug.NewStaticFiles` and `plug.SampleProcess`; `httpServer` over `server.Server` |
| `sampler.go` | `sampleProcess`: `process.Self` as the model says it (`model.Process`), the figures rounded as kit rounds them — moved from kit's `process.go` (ADR 0172) |
| `sampler_internal_test.go` | the sample is this process's; `Enable` plugs it in |
| `serverengine_compliance.go` | `var _ plug.HTTPServer = httpServer{}` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/kit/serverengine/ && GOWORK=off go test ./internal/kit/serverengine/ && GOWORK=off go test ./internal/kit/ -run 'Listener|Static|Frontend|NoSamplerNoProcess'
```
