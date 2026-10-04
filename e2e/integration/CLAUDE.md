<!-- updated: 2026-10-03T13:06:04Z -->
# e2e/integration/

## Purpose

The Docker-backed integration suites: the SDK's mechanisms and its vendor
integrations against REAL servers, started by testcontainers-go. Every file is
an `//go:build integration` test; there is no production code and nothing here
is part of the conformance binary (`e2e/main.go` imports none of it).

They sit in the auxiliary `e2e` module, outside `go.work` and outside Bazel
(`.bazelignore`), so testcontainers, moby and the database drivers they need
are required by `e2e/go.mod` alone and reach no module a consumer requires
(ADR 0157). Until that ADR they lived under `third-party/`: the SQL suite in
the root module, the writers' suites inside each writer's package — which, once
each vendor became a module of its own, would have put testcontainers in the
graph of every consumer of a database writer.

| Directory | Suite | Servers |
|---|---|---|
| `sql/` | the SQL mechanisms (`pkg/v1/data/sql`, `docstore`, `queue`) on the three engines — see `sql/CLAUDE.md` | SQLite (none), `postgres:17`, `mysql:8.4` |
| `writer/clickhouse/` | the `"clickhouse"` writer (`third-party/db/writer/clickhouse`) through `writer.Open`, rows read back | `clickhouse/clickhouse-server:24-alpine` |
| `writer/mysql/` | the `"mysql"` writer over the bind-mounted Unix socket, rows read back | `mysql:8` |
| `writer/redis/` | the `"redis"` writer over the bind-mounted Unix socket: XADD, MaxLen trimming, the `AddFailed` path | `redis:7-alpine` |

The writer suites are external test packages: they import the writer module
for its registration side effect and drive `internal/core/observe/logger/writer`'s registry,
exactly as they did beside the package (`e2e/go.mod` replaces each writer
module with this tree). That is the one place `e2e` imports more of `internal/`
than sentinel codes — see `e2e/CLAUDE.md` §Rules.

## Run procedure (rule 12)

No CI lane runs them — the runners do not start the containers — and the
census lanes (ADR 0137) that loop over `e2e` compile none of them: the default
build excludes every file by its tag. They are a named, manual lane. Run them
from the `e2e` module, with Docker (or a Docker-compatible runtime) reachable:

```sh
cd e2e
GOWORK=off go test -tags integration -race -timeout 1200s ./integration/...
GOWORK=off go vet -tags integration ./integration/...    # compiles them, no Docker needed
```

Each suite self-skips the cases whose server cannot start, and removes every
container it started. Run `sql/` on any change to `internal/service/data/sql`,
`internal/core/data/sql`, the SQL engine of `internal/service/data/docstore` or the SQL
broker of `internal/service/data/queue`; run `writer/<engine>/` before shipping a
change to that writer's factory or batching chain.

On macOS, the mysql and redis writer suites fail where the bind-mounted Unix
socket path exceeds the kernel's 104-byte `sun_path` (`connect: invalid
argument` under `$TMPDIR`), and the redis `MaxLen` case asserts an
approximate trim the server is free not to perform. Both reproduce
identically on the tree before ADR 0157; they are open, not caused by the
move.

## Do NOT

- Drop the `integration` tag from a file here: the census lanes would then
  compile testcontainers and the drivers on every platform cell, and run the
  suites wherever Docker happens to answer.
- Import any of this from the conformance binary (`e2e/main.go`, `checks/`).
- Move a suite back into a module of `go.work`: its test imports would put
  testcontainers in that module's `go.mod`, and so in its consumers' graph.

## Subtree

- `sql/` — see `sql/CLAUDE.md`
- `writer/clickhouse/`, `writer/mysql/`, `writer/redis/` — see each one's `CLAUDE.md` (the case, the server, what is open)
