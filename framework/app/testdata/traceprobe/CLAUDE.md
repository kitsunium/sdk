<!-- updated: 2026-10-22T00:00:00Z -->
# framework/app/testdata/traceprobe — a whole product, read for the tool's name

A test fixture, not a package a product imports: a product on `framework/app`
as a product writes one, every subsystem but the Studio imported —
`framework/app/{store,queue,workflow,mail,jobs,otlp,server}` — and one of each
building block declared: a store with a personal field that says whom a record
is about and a retention, a frontend served from embedded files (`site/`), a
topic and its subscription, a workflow, a mailer, two jobs (every hour, on a
cron expression), a command and a query exposed over HTTP, a webhook, a
setting and a secret, and an OTLP export (`app.OTLP()`, off until its
endpoint is set: its settings and its secret are read, nothing is sent).

`notrace_external_test.go`'s `TestAProductCarriesNoTraceOfTheTool` (ADR 0180
§6, step 6) reads its tree — every file but this prose, and the `go.mod` its
build reads —, builds it as a release is built (`go build -trimpath`, no tag,
no overlay) and reads the binary's symbols, string data and `go version -m`,
then runs it — `-h`, `graph`, `graph -format mermaid`, `config`, `secrets
list`, and served in production — its secrets in memory, its data key and
index key pinned, the same on every platform, and not on Windows nor plan9,
whose data directory the SDK refuses —, its probes, its frontend, its operations, its
webhook and the reserved route space asked — and refuses the tool's name
everywhere but in the binary's former names the test's allow-list admits.

## Contents

| File | Holds |
|---|---|
| `main.go` | the product: the service `Shop`, its building blocks, `App`, `main`, and the blank imports of the seven subsystems |
| `site/index.html` | the frontend, embedded |
| `README.md` | written by hand: a `main` under `testdata/` is no package docs/api records, so tools/genindex writes no README for it (ADR 0167) |

## Rules

- It imports `framework/app`, its six subsystems but the Studio, and the standard library only — never `framework/internal/app`: a product imports the facade.
- Every file the build reads carries no occurrence of the three letters the guard refuses, but inside the vendor's name: the guard reads them.
- Its README is written by hand and says what its package comment says: an edit of one is an edit of the other.

## Verify

```sh
cd framework && GOWORK=off go test ./app/ -run 'TestAProductCarriesNoTraceOfTheTool' -v
```
