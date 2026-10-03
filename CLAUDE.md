<!-- updated: 2026-10-03T04:00:00Z -->
# kitsunium/sdk

## Purpose

Go SDK providing a normed, performant toolbox for downstream applications: the mechanisms a service is made of — typed errors, observability, codecs, cryptography, storage, networking, process supervision and the application mechanisms above them — built on the standard library (ADR 0156) and published as one stable surface, `pkg/v1`. The kernel's generic primitives build `internal/core`'s ports, values and error codes, the core builds `internal/service`'s engines, a complete service is exported through `pkg/v1`, and the framework (ADR 0147) sits above `pkg/v1`. Every domain follows the principles of the charter (ADR 0154), and ADR 0155 groups the tree into the families the table names. One line per domain below; what each ADR decided is digested in `docs/adr/CLAUDE.md`, and every package documents itself in its own `CLAUDE.md`.

| Domain | Family (ADR 0155) | What it is | ADRs |
|---|---|---|---|
| `errs` | root | Typed errors: `Define` / `Wrap`, dotted-quad `MM.LL.PP.SS` codes, a wire-safe `Public` and a log-only `Private`, origin wins on wrap | 0002, 0005, 0006, 0019, 0020, 0035, 0160, 0161 |
| `clock` | root | The time port — `Clock`, `Waiter`, `Timed`, `System`, `ManualClock`; every SDK wait runs on an injected clock | 0039, 0090 |
| `crypto` | crypto | AEAD `Seal` / `Open` with a hidden nonce, and beside it hashes, MACs, KDFs, signatures, key agreement, password hashing (with `IsCommon`) and JWK/JWKS | 0013, 0014, 0143 |
| `secret` | security | A `Value` no rendering writes down; versioned stores (memory, environment, sealed file), a keyring and a rotator; one key per subject, destroyed to erase | 0096, 0142 |
| `redact` | security | Display redaction of values, JSON, text and log attributes — names, declared fields, URL credentials — within an exact byte bound | 0101 |
| `token` | security | JWT over JWS Compact and PASETO v4.public; the algorithm is bound by the constructor, never read from the token | 0042 |
| `session` | security | Server-side sessions: a self-redacting 256-bit identifier, memory and file stores, absolute and sliding expiry, `Regenerate` the only login | 0045, 0073 |
| `authz` | security | RBAC and ABAC with no policy language; abstention is the zero verdict, deny-overrides the one combiner | 0057 |
| `net` | net | TLS/mTLS identities, the guarded outbound client, the server engine and its drain signal, SSE, WebSocket (RFC 6455) and a static file tree | 0029, 0043, 0047, 0069, 0130 |
| `proc` | proc | Spawn and wait, signals, the reaper, rlimits, cgroups and the cap already bounding the process, sd_notify and socket activation, what the program was built from | 0016, 0075, 0093, 0100, 0144 |
| `ipc` | proc | A private socket between processes of one machine: the directory gates it, the kernel names the peer where it can | 0148 |
| `logger` | observe | Structured logger, one allocation per emit, trace-correlated; named writers (console, file, rotation, journald, database, network) and a `slog` bridge at the edge | 0012, 0015, 0030, 0032, 0062, 0070, 0132 |
| `metrics` | observe | The OpenTelemetry metrics data model from its specification, zero OTel imports; text, Prometheus and OTLP/JSON exporters | 0027, 0044, 0048, 0067 |
| `trace` | observe | The OTel span model and W3C Trace Context from their documents, sampled once at the root, exported as OTLP/JSON | 0051 |
| `profiling` | observe | The process's own CPU, heap and goroutine profiles, decoded with the standard library and folded onto owners the caller names | 0121 |
| `codec` | data | 24 wire formats behind one `Marshal` / `Unmarshal` dispatch with per-format facades, strict JSON decoding, a type's wire shape and JSON patch | 0003, 0021, 0022, 0023, 0036, 0037, 0102, 0133, 0134, 0143, 0156 |
| `transform` | data | Compression: stdlib gzip, raw DEFLATE and zlib; zstd and s2 opt-in under `third-party/`, with a ceiling that cannot be omitted | 0014, 0066 |
| `sql` | data | Ports over `database/sql`, never an ORM or a driver: structural transaction ownership, savepoints, migrations under a lock that dies with its holder | 0055, 0139, 0140 |
| `docstore` | data | Typed keyed JSON documents with indexes and versions — one overlay entry per write through `vfs`, or tables of the caller's database | 0110, 0139, 0143 |
| `queue` | data | Durable at-least-once queue over a directory, a SQL table joined to the caller's transaction, or memory; retried and dead-lettered | 0054, 0104, 0151 |
| `cache` | data | The kernel LRU+TTL primitive and the domain above it: tag invalidation, in-process stampede protection, L1/L2 chaining | 0025, 0049 |
| `vfs` | data | `io/fs` reading unchanged and atomic publication, with how far "atomic" reaches stated | 0056 |
| `config` | app | Environment, file and `fs.FS` layering, typed decode, a schema (required keys, defaults, unknown keys refused), provenance, a poll watcher | 0028, 0061, 0097 |
| `cli` | app | Command lines on the stdlib `flag`: sub-commands, generated help, a typed exit status, a seam to `config` | 0065 |
| `i18n` | app | Message translation over a named 13-language CLDR subset; every other language refused by name | 0063 |
| `validation` | app | A `Constraint` port, located violations and the engine composing them; a message never names the value | 0046 |
| `view` | app | Server-side rendering on `html/template`: one trust type, templates parsed once, `text/template` refused | 0058 |
| `events` | app | An in-process synchronous event bus keyed on Go types — not a queue | 0053 |
| `scheduler` | app | Five-field POSIX cron and fixed intervals, with DST, missed deadlines and overlap decided | 0041 |
| `statemachine` | app | Entities moved by events, timers, deadlines and guards over the caller's store, on an agenda rather than a sweep | 0120 |
| `resilience` | app | Retry, circuit breaker, keyed rate limit, bulkhead, timeout, fallback, hedging, and the published backoff curve | 0026, 0031, 0103 |
| `lifecycle` | app | Ordered start and reverse stop with a per-component budget, and the supervisor that restarts a loop | 0050, 0112 |
| `health` | app | Startup, readiness and liveness as three types, every wait bounded, and the loopback readiness probe | 0060, 0072, 0131 |
| `lock` | app | Named exclusive leases over one process or one machine, with fencing tokens and the guarantee that is not made said out loud | 0052, 0081, 0082, 0083, 0084, 0086 |
| `id` | app | UUIDv4/v7, ULID, snowflake, NanoID, KSUID and TypeID | 0024, 0038 |
| `mail` | app | MIME composition and SMTP with header injection refused, and a durable spool | 0064, 0111, 0141 |
| `selfupdate` | framework (ADR 0158) | A signed release verified before its digest, then an atomic replacement; no key, no install | 0077, 0150 |
| `entitlement` | framework (ADR 0158) | A vendor-signed roster read into a grant, with an offline cache, an anti-rollback ratchet and a CI seat | 0078, 0079, 0091, 0092 |
| `gate` | framework (ADR 0158) | Whether an invocation is subject to the check: a policy value and a decision that performs nothing | 0080 |
| `git` | framework (ADR 0158) | What a branch changed and what a working tree is at, through hardened git invocations; it degrades, never empties | 0076, 0087, 0100 |

**Repository**: `github.com/kitsunium/sdk` · **Module name**: same · **Go**: 1.27.1 (pinned in `MODULE.bazel`)

## Architecture at a glance

```
internal/
├── kernel/        stdlib-only AND generic primitives
│                  backoff, batcher, buffer, cache, clock, errs, group,
│                  heap, pathchain, plugin, recycler, ring, singleflight,
│                  snapshot, worker
├── core/          domain interfaces + domain values
│                  authz, cache, cli, codec (+ scratch), config, crypto,
│                  entitlement, events,
│                  gate,
│                  health, i18n, id, lifecycle, lock, logger, logger/level,
│                  mail, metrics, net, otel, proc, queue, resilience, scheduler,
│                  secret, selfupdate, session, sql, statemachine, token,
│                  trace, transform,
│                  validation, vcs,
│                  vfs,
│                  view, writer
└── service/       concrete implementations
                   authz  (RBAC + ABAC + deny-overrides + Check)
                   cache   (tagged memory store + L1/L2 chain)
                   cli    (resolution loop + generated help + config seam)
                   config (env, file and fs.FS sources + layered decode +
                           schema + origins + poll watcher)
                   events  (synchronous priority-ordered bus + On[E])
                   health (check registry + per-check timeouts + drain latch
                           + HTTP handler + the loopback Ask)
                   lifecycle (ordered engine + per-component stop budget
                              + opt-in signal/sd_notify Run + the supervisor)
                   docstore (typed JSON documents + unique/multi indexes;
                             one overlay entry per write, one snapshot at rest;
                             + the same store over SQL, joined to the
                             context's transaction — ADR 0139; + a document's
                             versions, in its own write — ADR 0143)
                   lock    (in-process leases + file locker over flock(2)
                            or LockFileEx + keepalive)
                   logger (+ encoder, sink/{console,file,memory,syslog},
                             middleware/{async,encwrite,failover,multi,
                                         recover,route,sample,tee})
                   writer (console, dbsink, file, journald, levelgate,
                           nettransport, rotfile)
                   crypto (aesgcm, commonpw, ecdsasig, ed25519sig,
                           hkdfsha256, hmacsha2, jwk, keyenvelope, keytree,
                           pbkdf2pw, stdhash, streamaead, x25519)
                   codec  (asn1, baseenc, bson, cbor, csv, flatbuffers,
                           form, json, msgpack, multipart, ndjson, pem, tlv, toml,
                           xml, yaml; + strictjson, a decoder and not a Format;
                           + jsonshape, a type's wire shape, not a Format;
                           + jsonpatch, two documents' difference, not a Format)
                   queue  (file, SQL and memory brokers + Consume loop
                           waiting on the Waker sibling; the SQL broker
                           joins the context's transaction — ADR 0151)
                   proc   (cgroup, childwait, exec, memlimit, reaper,
                           rlimit, sdlisten, sdnotify, self, signal)
                   i18n   (CLDR plural table + catalogue + negotiator + printer)
                   profiling (CPU/heap capture + a stdlib pprof decoder +
                           fold onto owners + goroutine dumps, grouped)
                   id     (uuidv4, uuidv7, ulid, snowflake, nanoid,
                           ksuid, typeid)
                   net    (tlsid, client, server, sse, websocket, static)
                   mail   (MIME composition + SMTP + memory and capture
                           doubles; spool/ — the durable outbox)
                   metrics (in-memory meter + text/prometheus/otlpjson
                             exporters + the OTLP/HTTP emitter)
                   resilience (retry, circuit breaker, rate limit + keyed,
                           bulkhead, timeout, fallback, hedging; the backoff
                           curve, an alias of kernel/backoff's)
                   scheduler (cron parser + fixed interval + engine)
                   secret (memory, environment and sealed file stores,
                           keyring over versions, rotator; subject keys
                           under a rotating root, destroyed to erase)
                   session (memory store, file store, AEAD cookie sealer)
                   statemachine (declarations + per-entity transitions +
                           an agenda heap the loop sleeps on)
                   sql    (transaction manager + savepoints + pool policy
                           + Join/Defer siblings + migration runner, SQLite's
                           on the database file's write lock — ADR 0140)
                   token  (JWS compact + PASETO v4.public)
                   trace  (tracer + samplers + recorder + otlpjson
                             exporter + the OTLP/HTTP emitter + the
                             server/client HTTP middlewares)
                   transform
                   validation (constraints + combinators + struct-tag plan)
                   view   (html/template engine + trust scan + parse-once)
                   vfs    (os.Root-confined FS + memory FS + atomic publish)
                   vcs    (git changed-set: merge-base + index + worktree
                           + untracked, hardened invocations; + Head)
                   redact (display redaction: names, declared fields, URL
                           credentials, within an exact byte bound)
                   selfupdate (signed release -> verified archive -> atomic
                           replacement, with consent and escalation opt-ins)
                   entitlement (vendor-signed roster -> grant, with an
                           offline cache, an anti-rollback ratchet and a CI seat)
                   gate   (may this invocation run? a policy value and a pure
                           decision; it verifies nothing and exits nothing)
                   ipc    (a private socket between processes of one machine:
                           the directory gates, SO_PEERCRED on Linux, a
                           named pipe with its own DACL on Windows — ADR 0148)
                   internal (otlp — the OTLP/HTTP + JSON transport metrics
                           and trace share; logfile — the hardened open both
                           file sinks share)
pkg/
└── v1/            stable public API (type aliases + ergonomic helpers)
    ├── cache/     (the ADR 0025 primitive AND the ADR 0049 domain, side by side)
    ├── clock/     (the time port: Clock/Waiter/Timed + System + ManualClock — ADR 0090)
    ├── logger/    (+ ldflags-injected Version, + writer/, + slogbridge/)
    ├── errs/      (construction + introspection: New, Wrap, CodeOf, …)
    ├── events/    (in-process synchronous bus — ADR 0053; NOT a queue)
    ├── codec/     (blank-imports all 16 service codecs + transform;
    │                 + strictjson/ — one document read one way — ADR 0102;
    │                 + jsonshape/ — a type's wire shape — ADR 0133;
    │                 + jsonpatch/ — two documents' difference — ADR 0143;
    │                 + json/, yaml/, toml/, bson/ — one format each — ADR 0134;
    │                 bson/ also names BSON's value types)
    ├── crypto/    (AEAD and keys; its scheme facades are siblings, not children:
    │                 agree/, hash/, kdf/, mac/, sign/, and password/ — IsCommon
    │                 since ADR 0143)
    ├── id/        (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID — ADR 0024, ADR 0038)
    ├── lifecycle/ (ordered start, reverse stop, per-component budget — ADR 0050;
    │                 the supervisor — ADR 0112)
    ├── config/    (env, file and fs.FS layering, schema, origins, poll watch — ADR 0028, ADR 0061, ADR 0097)
    ├── health/    (startup, readiness and liveness — three questions, three types — ADR 0060;
    │                 Ask, the loopback readiness probe — ADR 0131)
    ├── resilience/ (retry, breaker, rate limit + keyed, bulkhead, timeout, fallback, hedging — ADR 0026, ADR 0103)
    ├── docstore/  (typed JSON documents, indexes, one entry per write — ADR 0110;
    │                 the same store over SQL, OpenSQL — ADR 0139;
    │                 a document's versions, in its own write — ADR 0143)
    ├── lock/      (Locker/Lease/Deadliner + memory & file lockers — ADR 0052, ADR 0081, ADR 0082, ADR 0083)
    ├── proc/      (its facades are siblings: cgroup, memlimit, process, reaper, rlimit,
    │                 sdlisten, sdnotify, signal; process also reads the process itself
    │                 — Self, Build — ADR 0100)
    ├── metrics/   (the OTel data model, zero OTel imports — ADR 0044)
    └── scheduler/ (Parse/ParseInLocation/Every + the engine — ADR 0041)
    └── server/    (+ sse/ — ADR 0029/0043, + websocket/ — ADR 0047,
                     + static/ — a file tree served by name — ADR 0130)
    └── client/    (the guarded outbound HTTP client, posture enforced by the transport — ADR 0029)
    └── tlsid/     (TLS and mutual-TLS identities, shared by server and client — ADR 0029)
    └── secret/    (a Value no rendering writes down, versioned stores, keyring, rotator — ADR 0096;
                     one key per subject, destroyed to erase — ADR 0142)
    └── statemachine/ (entities moved by events, timers, deadlines, guards; an agenda, not a sweep — ADR 0120)
    └── profiling/ (the process's CPU, heap and goroutines, folded onto your owners — ADR 0121)
    └── session/   (memory + file Store, AEAD Sealer, Regenerate — ADR 0045)
    └── token/     (JWT over JWS compact + PASETO v4.public — ADR 0042)
    └── trace/     (W3C Trace Context + the OTel span model — ADR 0051)
    └── validation/ (Constraint / Violation / Report + the struct-tag front end — ADR 0046)
    └── authz/      (RBAC + ABAC, no DSL, abstention is the zero — ADR 0057)
    └── i18n/       (CLDR plurals over a named 13-language subset — ADR 0063)
    └── cli/        (flag + sub-commands + generated help + typed exit — ADR 0065)
    └── mail/       (compose + Transport, injection refused — ADR 0064;
                     the spool, the durable outbox — ADR 0111)
    └── queue/      (durable at-least-once broker, no lock — ADR 0054)
    └── sql/        (ports over database/sql, no driver, no ORM — ADR 0055;
                     Joiner/Deferrer — ADR 0139; SQLite migrations — ADR 0140)
    └── vfs/        (io/fs reading unchanged + atomic publication — ADR 0056)
    └── git/        (what a branch changed; degrades, never empties — ADR 0076)
    └── selfupdate/ (signature THEN digest THEN disk; no key, no install — ADR 0077)
    └── entitlement/ (signed roster -> grant; bring your own Identity — ADR 0079)
    └── gate/        (may this invocation run? decides, performs nothing — ADR 0080)
    └── ipc/         (a private socket, the peer the kernel names — ADR 0148)
    └── redact/      (secrets replaced for display, exact bound — ADR 0101)
    └── view/       (html/template, one trust type, parse once — ADR 0058)
third-party/       opt-in vendor integrations, one Go module per vendor
                   (ADR 0157) — see third-party/CLAUDE.md:
                   aws (writer/{cloudwatch,s3}), codec/hcl, codec/protobuf,
                   db/writer/{clickhouse,mysql,redis},
                   transform (zstd + s2 — ADR 0066),
                   x-crypto (argon2id, xchacha);
                   entitlement (the ssh Identity + enrolment ONLY; the
                     mechanism is pkg/v1/entitlement — ADR 0079), still in
                     the root module until it leaves for the framework
                     (ADR 0158);
                   the SQL mechanisms' suites on real engines moved to
                     e2e/integration/sql (ADR 0139/0140, ADR 0157)
framework/         the layer above pkg/v1, its own module — ADR 0147
                   model (the graph types + the ID grammar, Version 5),
                   kit (the runtime a product imports), telemetry (the
                     telemetry port + exporter — ADR 0149),
                   connectors/{postgres,mysql,sqlite} (one driver, one module each)
```

`baseenc` is NOT a `pkg/v1/codec` subpackage — it is a service codec
(`internal/service/codec/baseenc`) registering nine base-N Formats
(base16/32/45/58/62/64/64url, hex, ascii85) through the same registry as
every other codec.

- Seventeen SDK modules held together by `go.work` — plus three **auxiliary** modules deliberately kept OUT of it: `e2e/`, `tools/genindex/` and `tools/sdkguard/`. Bazel's `go_deps` extension reads `go.work` and cannot process extra modules, so adding any of them breaks the build; they carry `replace` directives (or need none) and are built with `GOWORK=off`. Counting `go.mod` files therefore yields twenty — that is not drift, it is the invariant — and `bash scripts/ci/go-modules.sh` prints them: it is the census every lane that loops over modules reads, so a module git tracks is built, vetted, tested on 32 bits and scanned without anybody editing a workflow (ADR 0137). A pattern never crosses a module boundary — `go build ./...` at the repository root builds the root module alone, in workspace mode or not — so a check of the whole SDK loops over that census. See `e2e/CLAUDE.md` §Do NOT and `tools/CLAUDE.md` §Do NOT. The seventeen workspace modules are: the root (the workspace's anchor beside `go.work` and `MODULE.bazel`: required by nothing and never tagged, it still holds entitlement's ssh `Identity` until that leaves for the framework — ADR 0157, ADR 0158), `internal/kernel`, `internal/core`, `internal/service`, and the public `pkg` (module `github.com/kitsunium/sdk/pkg`, `go.mod` at `pkg/go.mod`; its consumer packages live under `pkg/v1/` and import as `…/pkg/v1/*`, but the *module* is the bare `…/pkg` because Go forbids a `/v1` module-path suffix — ADR 0017), the **framework** (module `github.com/kitsunium/sdk/framework`, `go.mod` at `framework/go.mod` — the layer above `pkg/v1` a product imports, which reaches `internal/` only through `pkg/v1` and `kernel/errs`, owns layer `4` of the dotted-quad and is released in lockstep with `pkg` — ADR 0147) and its three database engines `framework/connectors/{postgres,mysql,sqlite}`, one driver and one module each, and the eight vendor modules under `third-party/` — `aws`, `codec/hcl`, `codec/protobuf`, `db/writer/{clickhouse,mysql,redis}`, `transform` and `x-crypto`, one vendor each (ADR 0157). All of them but the root are released in the same lockstep. Each module-local `go.mod` carries `replace` directives so `GOWORK=off go build ./...` per-module still works. Each vendor lives in its own module's `go.mod` — nothing in the SDK requires a vendor module, so `pkg` consumers stay dep-light and a consumer of one integration takes that vendor's graph and no other's.
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1 → framework, with `third-party/` above all but the framework. It is enforced on the BUILD GRAPH by `scripts/check-layer-deps.sh` (in `make lint` and CI — ADR 0068), not by visibility: ADR 0004 put it on `package_group` + `visibility`, but Gazelle gives every package under `internal/` the visibility `//:__subpackages__`, which admits the whole repository, and a kernel package importing core was shown to build. A rogue import fails `make lint` and CI, naming the target it reached.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall, which is also what keeps them out under Bazel.
- Build / test / lint go through **Bazel 9** — see ADR 0004. `go test ./...` still works locally for quick iteration but CI only runs `bazel`. Because the two build systems disagree about what is in scope, anything excluded from one MUST be covered by the other — see rule 12.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make lint` (mod-tidy + gazelle drift + gofumpt -l + ktn-linter + alloc-lane coverage + audit coverage + domain-doc drift + package docs + BENCH.md presence + error-code drift + layer firewall + `make guard` + `make doclinks`) |
| Vulnerability scan | `make vuln-install && make vuln-check` — govulncheck over every module, fails on a reachable vulnerability (ADR 0136); online, so not part of `make lint` |
| Local test suite | `make build && make test` (build prep + race tests) |
| Allocation gates | `make test-alloc` — race-off pass; the ONLY lane that runs `//go:build !race` tests (targets in `tools/alloc-lane-targets.txt`, see rule 12) |
| Single-package test | `bazel test //<path>:<target>` (e.g. `bazel test //internal/kernel/errs:errs_test`) |
| Regenerate BUILD.bazel | `bazel run //:gazelle` after changing imports or `go.mod` |
| Coverage | `bazel coverage --combined_report=lcov //...` — LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| Release dry-run | `make release-dry-run` (computes patch bumps locally without pushing tags — see ADR 0007) |
| Regenerate READMEs | `make docs-readme` (regenerates the `README.md` of every `pkg/v1` package declaring `//go:generate gomarkdoc` — all 67 today — and of every package of the `framework` module declaring one (not the `framework/connectors/*` modules, which that `go generate` does not reach), from its package doc comment; see ADR 0008) |

Branch naming matches the conventional commit prefix: `feat/*`, `fix/*`, `refactor/*`, `chore/*`, `docs/*`.

## SDK-wide rules (non-negotiable)

1. **Kernel gate.** A kernel package MUST be stdlib-only AND generic (no domain vocabulary). `level` was moved OUT of kernel because it fails the second half — see ADR 0002 / the layer-placement audit in `.claude/contexts/sdk-layer-placement-audit.md`.
2. **Typed errors only.** Every error returned from SDK code goes through `errs.Define` or `errs.Wrap` (`internal/kernel/errs`). `fmt.Errorf` / `errors.New` are banned in production code — every non-`_test.go` file under `internal/`, `pkg/`, `third-party/` and `framework/`, with no exemption. `make guard` enforces it: `tools/sdkguard`'s SDK002 over that tree, failing `make lint` and CI's lint gate on either call and on a `//sdkguard:allow SDK002` directive (SDK002 stays a convention for consumers — ADR 0033). `errors.Is` / `As` / `AsType` / `Join` / `Unwrap` and `errors.ErrUnsupported` stay allowed.
3. **Dotted-quad error codes.** `Code` is a `uint32` laid out `MM.LL.PP.SS` (Major / Layer / Package / Serial) — see ADR 0005. Each package owns a `PP` slot; ADR 0005 §Registry + the ADR 0006 extension (logger v2 + ring) are the authoritative allocation table. Two AST audits enforce it: `registry_external_test.go` checks that no two `Define` calls resolve to the same **value**, and `registry_ownership_external_test.go` checks **range ownership** — one package per `MM.LL.PP`, against a hand-maintained `codeRangeOwners` table that is deliberately independent of the constants it audits (ADR 0035). A new range is allocated in that table in the same change that introduces its codes. Both audits only judge files that reach them as runfiles of `//:audit_sources`, so `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring codes is missing from that list — the gap ADR 0020 records having already hidden ~10 emitters, and which had silently reopened for all five `internal/service/net/*` packages. Match codes with `errs.HasCode(err, CodeX)` (walks `Unwrap() error` *and* `Unwrap() []error`) or `errors.Is(err, errs.NewPrefixMatcher(...))` for subnet-style routing.
4. **Public/Private split.** Every SDK error carries a wire-safe `Public` (string literal ≤120 runes, no newline) and a log-only `Private`. `err.Error()` renders `"[<code> <REASON>] <public>"` on the no-trail fast path; when the wrap trail is non-empty, ADR 0005 §Semantics extends the bracket header with `" <- "`-separated trail codes and an optional `" (truncated)"` marker — never Private, never Fields. Log-parser regex: `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`.
5. **No empty stub files / dirs.** If a file or directory only carries a placeholder, inline its content into an existing file or delete it.
6. **Origin wins on wrap.** When `errs.Wrap` receives an `*errs.Error` cause, it inherits the cause's Code/Reason/Public/Private. Wrappers can only add `Fields` (and extend the intrinsic wrap trail). To relabel, define a fresh sentinel.
7. **`Version` via build-time injection.** `pkg/v1/logger.Version` is stamped at link time — under Bazel via `x_defs` + `--stamp` + `tools/workspace_status.sh` (`STABLE_VERSION`); under raw `go build` via `-ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=…"`. `FrameworkVersion()` returns `"dev"` when unset; every emitted log record carries `framework_version` automatically.
8. **Every package is documented.** The guard `scripts/pre-commit/check-pkg-docs.sh` — run by `make lint` and CI — fails when any `internal/*` or `pkg/v*/**` directory containing Go production code is missing `CLAUDE.md` AND `README.md`. Public packages (`pkg/v*/**`) require BOTH: `README.md` (consumer-facing — pkg.go.dev renders it; the model is `pkg/v1/errs/README.md`) and `CLAUDE.md` (agent-facing), because the two roles do not collapse. The `scripts/release/*.{sh,mjs}` and `docs/site/scripts/*.mjs` trees are tooling, not library code, and are exempt from this gate.
9. **Every benchmark package ships its numbers.** The guard `scripts/pre-commit/check-bench-md.sh` — run by `make lint` and CI — fails when a directory contains `*_bench_test.go` but no sibling `BENCH.md`. The report is regenerated with `make bench`; it stamps machine, RAM, CPU, OS, Go toolchain, git SHA, and timestamp so cross-machine deltas can be evaluated honestly.
10. **`pkg/v*/**/README.md` are generated, not hand-authored.** The `gomarkdoc` binary (`go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0`) reads each package's Go doc comments and emits `README.md` per package (ADR 0008). Edit the package comment in the existing `.go` file (`codec.go` / `accessors.go` / `logger.go`); run `make docs-readme` to regenerate; `scripts/pre-commit/check-readme-drift.sh` — run by CI's `bazel` job — blocks any change where the file on disk doesn't match what gomarkdoc would produce now. Maintainer rationale (Why-this-shape, layering, do-not lists) stays in `CLAUDE.md` — consumer-facing prose belongs in the package doc comment.
11. **Docs travel with the code — always update them in the same change.** Documentation is part of the change, never a follow-up. Whenever you add/rename/remove an exported symbol, package, format, code range, capability, or convention, update every doc that describes it **in the same commit**: the package's `CLAUDE.md` (Purpose/Surface/Contents/Sentinels), the parent/layer `CLAUDE.md` tables (e.g. `internal/service/codec/CLAUDE.md` Streaming/Appender columns, `internal/core/CLAUDE.md` registry counts), the root `CLAUDE.md` domain list, and — for public packages — the Go doc comment that `gomarkdoc` renders into `README.md` (rule 10). A doc that names a symbol, count, file, or code that no longer matches the code is a defect: fix the doc or the code, never leave them divergent. When in doubt, grep the docs for the old name/number before committing. Two of this rule's failure modes are now mechanical rather than remembered: `scripts/pre-commit/check-domain-docs.sh` fails the build when this file's architecture tree stops naming exactly the `internal/core` directories, when a domain is described twice in the Purpose paragraph, or when one of the three ADR indexes (`docs/adr/CLAUDE.md`, `docs/CLAUDE.md`, this file's Reference list) drifts from the files in `docs/adr/` — both of which parallel union merges had actually produced here, along with two whole Purpose paragraphs coexisting while `events` was documented only in the stale one and `health` in neither.
12. **Every test excluded from normal discovery needs a named, executable, currently-green gate — or an explicit declaration that it must not run.** Exclusion mechanisms compound silently: `//go:build !race` hides a file from the race suite (race is on by default, see `.bazelrc`), `gazelle:excluded` + `manual` + `-test.run=^$` hides a target from `bazel test //...`, and a `.ktn-linter.yaml` entry hides it from the linter. Each exclusion is individually justified and documented; *together* they have already produced tests that nothing ever ran — including `pkg/v1/logger`'s `TestV116BuildSendAllocatesOnePerEmit`, the regression guard for the allocation claim in this very file. Gates in force today: the race-off alloc lane (`tools/alloc-lane-targets.txt`, mechanically enforced by `scripts/pre-commit/check-alloc-lane-coverage.sh`, wired into `make lint` and CI) covers every `//go:build !race` test, and CI's `test-386` job compiles and runs those same files a second time on linux/386, where `-race` does not exist; `TestGenerateBenchMD` is opt-in by exact `-test.run` name under BOTH build systems; `integration` tagged suites declare their run procedure in `e2e/integration/CLAUDE.md` (they live in the auxiliary `e2e` module — ADR 0157) and `localstack` ones in their package `CLAUDE.md`. `//framework/internal/kit:kit_test` is `manual` under Bazel — the suite reads its own sources and positions relative to its module root — and `make test-framework` (`go test -race` in every framework module, a step of CI's `bazel` job and a gate of `scripts/ci-gates-check.sh`) is its lane (ADR 0147). When you add an exclusion, name its compensating lane in the same commit — and remember that a lane which exists but has been failing for weeks verifies nothing.

## Layout

```
sdk/
├── internal/              see internal/CLAUDE.md
├── pkg/v1/                see pkg/CLAUDE.md + pkg/v1/CLAUDE.md
├── third-party/           opt-in vendor integrations, one Go module per vendor — see third-party/CLAUDE.md (ADR 0157)
├── framework/             the framework module above pkg/v1 — see framework/CLAUDE.md (ADR 0147)
├── docs/                  ADRs — see docs/CLAUDE.md
├── .github/               CI workflows — bazel-ci.yml is the SDK lane
├── go.work, go.mod        workspace + the root module, its anchor (read by Bazel via from_file)
├── MODULE.bazel           Bzlmod entry point (rules_go 0.60.0 + gazelle 0.50.0 + go_sdk 1.27.1 + go_deps)
├── BUILD.bazel            root gazelle target + audit_sources filegroup
├── .bazelrc               race-on by default; named configs: race / pure / ci / alloc
├── .bazelversion          pins Bazel to 9.0.2
├── Makefile               build / test / test-alloc / lint / bench / cover / docs / serve / release-dry-run / docs-readme … (run `make` for the full list)
├── scripts/               guards (scripts/pre-commit/, run by make lint and CI — ADR 0153), release (scripts/release/) and CI (scripts/ci/) scripts, the layer firewall (check-layer-deps.sh)
├── .ktn-linter.yaml       ktn-linter configuration — the phases 1-7 gate `make lint-ktn-check` runs
├── e2e/                   real-kernel conformance harness + the Docker-backed integration suites (e2e/integration) — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── tools/sdkguard/        consumer-facing rule enforcement (stdlib-only CLI — ADR 0033)
├── tools/workspace_status.sh  prints STABLE_VERSION (consumed by --stamp + x_defs)
├── tools/alloc-lane-targets.txt  target list for the race-off alloc lane (rule 12)
├── tools/genindex/        docs-site symbol index — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── .golangci.yml          code-quality second-opinion linters (the layer firewall is scripts/check-layer-deps.sh — ADR 0068)
├── AGENTS.md, agent.toml  agent specs (not SDK)
└── README.md              the SDK quickstart (packages, install, verification)
                           — per-package docs live next to their code
```

## Verification

| Command | Expected |
|---|---|
| `ktn-linter lint ./...` | No issues found |
| `bazel build //...` | all targets pass |
| `bazel test --config=race //...` | every `*_test` target green (race on by default — see `.bazelrc`) |
| `bazel coverage --combined_report=lcov //...` | LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| `bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'` | empty — kernel has zero outgoing go_library edges |
| `bash scripts/check-layer-deps.sh` | exit 0 — no layer reaches one above it: kernel → core → service → pkg/v1 → framework, `third-party/` above all but the framework (ADR 0068, ADR 0147; visibility alone does not bind `internal/` packages) |
| `make test-framework` | every framework module's suite green under `go test -race`, `GOWORK=off` — the gate of the Bazel-`manual` `//framework/internal/kit:kit_test` (ADR 0147) |
| `make build` | `bazel mod tidy` + `bazel run //:gazelle` + `gofumpt -l -w` + `bazel build //...` |
| `make test` | every `*_test` target green incl. `//internal/kernel/errs:errs_test` (AST audit) |
| `make test-alloc` | race-off allocation gates green — every target in `tools/alloc-lane-targets.txt`, the only lane running `//go:build !race` tests |
| `bash scripts/pre-commit/check-alloc-lane-coverage.sh` | exit 0 — no `!race` test sits outside `tools/alloc-lane-targets.txt` (rule 12) |
| `bash scripts/pre-commit/check-domain-docs.sh` | exit 0 — the architecture tree still names exactly the `internal/core` directories, no domain is described twice, and the three ADR indexes name exactly the ADRs on disk, once each (rule 11) |
| `bash scripts/pre-commit/check-audit-coverage.sh` | exit 0 — no package declaring an `errs.Define`/`errs.Code` sits outside `//:audit_sources` (rule 3) |
| `GOWORK=off GOARCH=386 CGO_ENABLED=0 go test ./...` (in every module `bash scripts/ci/go-modules.sh` prints) | green — the 32-bit RUNTIME bar, run by CI's `test-386` job over the whole census, `tools/` and the root module included (ADR 0137). `cross-build` proves the SDK compiles on 386; this proves it behaves, which is where a `int(0xffffffff)` read as `-1` shows up |
| `make vuln-install && make vuln-check` | every module: no REACHABLE known vulnerability (`govulncheck` source mode, pinned in the `Makefile`; needs `vuln.go.dev`). Blocking in CI's `bazel` job and run daily by `vuln-scan.yml` (ADR 0136) |
| `cd pkg && go test ./...` | green; `pkg/v1/codec` completes in seconds — `TestGenerateBenchMD` self-skips unless named via `-run` |
| `make lint` | drift assertion (read-only): mod tidy + gazelle diff + gofumpt -l + `make guard` + `make doclinks` + ktn-linter + alloc-lane coverage + audit coverage + domain-doc drift + package docs + BENCH.md presence + error-code drift + layer firewall (`scripts/check-layer-deps.sh`) |
| `make guard` | `tools/sdkguard` over the SDK's own tree: the invariants (ADR 0033), then SDK002 over `internal/`, `pkg/`, `third-party/` and `framework/` — rule 2, no exemption (ADR 0161); no network — `-version-check=off` |
| `make doclinks` | every same-package doc link in the repository names a symbol its package declares — a member of an aliased type is written `[Type].Member` (ADR 0138); part of `make lint-check`, so CI runs it |

## Reference

The full digest of every ADR — what it decided, what it amends, its status — is the Contents table of `docs/adr/CLAUDE.md`; each ADR file is authoritative. This list names them, one line each.

- ADR 0001 — SDK Go Multi-Module Layout — `docs/adr/0001-sdk-go-multimodule-layout.md`
- ADR 0002 — SDK `errs` Package (Layered Typed Errors) — `docs/adr/0002-sdk-errors-package.md`
- ADR 0003 — SDK `codec` Package (Universal Encode/Decode Surface) — `docs/adr/0003-sdk-codec-package.md`
- ADR 0004 — Bazel 9 as the single build/test system — `docs/adr/0004-sdk-bazel-build-system.md`
- ADR 0005 — Dotted-Quad Error Codes (supersedes ADR 0002 §Registry) — `docs/adr/0005-sdk-error-codes-dotted-quad.md`
- ADR 0006 — Error Code Registry Extension (logger v2 + ring) — `docs/adr/0006-sdk-error-code-registry-extension.md`
- ADR 0007 — SDK release workflow and versioning policy — `docs/adr/0007-sdk-release-and-versioning.md`
- ADR 0008 — README generation from Go doc comments — `docs/adr/0008-readme-from-code-generation.md`
- ADR 0009 — Public module must be `go get`-resolvable and accessible — `docs/adr/0009-pkg-public-module-resolvability.md`
- ADR 0010 — Kernel object-recycling primitive — `docs/adr/0010-kernel-recycler-primitive.md`
- ADR 0011 — Kernel copy-on-write snapshot primitive — `docs/adr/0011-kernel-snapshot-primitive.md`
- ADR 0012 — Logger writer registry (named, config-driven Sink factories) — `docs/adr/0012-logger-writer-registry.md`
- ADR 0013 — Crypto domain: authenticated encryption (`Seal` / `Open`) — `docs/adr/0013-sdk-crypto-domain.md`
- ADR 0014 — The verb wave: transform tier, new crypto ports, config-driven topology — `docs/adr/0014-sdk-transform-crypto-ports-config-topology.md`
- ADR 0015 — Writer taxonomy, depTier as a first-class writer property, default rotation policy, and a dep-light DB/transport gate — `docs/adr/0015-sdk-logger-writer-taxonomy-and-rotation.md`
- ADR 0016 — OS process-supervision domain (`proc`) — `docs/adr/0016-sdk-process-supervision-domain.md`
- ADR 0017 — Public module is the bare `pkg`, not `pkg/v1` (Go forbids the `/v1` suffix) — `docs/adr/0017-pkg-bare-module-path.md`
- ADR 0018 — Cross-platform portability strategy — `docs/adr/0018-sdk-cross-platform-portability.md`
- ADR 0019 — Public error construction API + third-party `MM` (Major) code assignment — `docs/adr/0019-pkg-errs-public-construction.md`
- ADR 0020 — errs audit accepts two Reason derivations (var-name OR Code-const); close the coverage gap — `docs/adr/0020-errs-audit-dual-reason-derivation.md`
- ADR 0021 — BSON codec (M5) — `docs/adr/0021-sdk-codec-bson.md`
- ADR 0022 — HCL codec, quarantined under third-party/ (M5) — `docs/adr/0022-sdk-codec-hcl.md`
- ADR 0023 — Schema codecs (M6): opt-in, under third-party/codec — `docs/adr/0023-sdk-schema-codecs.md`
- ADR 0024 — Identifier-generation domain (`id`) — `docs/adr/0024-sdk-id-domain.md`
- ADR 0025 — Generic LRU+TTL cache as a kernel primitive (`cache`) — `docs/adr/0025-sdk-cache-kernel.md`
- ADR 0026 — Reliability domain (`resilience`) — `docs/adr/0026-sdk-resilience-domain.md`
- ADR 0027 — Observability domain (`metrics`) — `docs/adr/0027-sdk-metrics-domain.md`
- ADR 0028 — Configuration domain (`config`) — `docs/adr/0028-sdk-config-domain.md`
- ADR 0029 — Network domain (`net`): unified inbound + outbound transport — `docs/adr/0029-sdk-net-domain.md`
- ADR 0030 — stdout is a protocol channel: no SDK default writes to it — `docs/adr/0030-stdout-is-a-protocol-channel.md`
- ADR 0031 — A policy's zero value is a safe default or an explicit refusal, never an inert policy — `docs/adr/0031-policy-zero-values-are-never-inert.md`
- ADR 0032 — slog is an adapter at the public edge, never a dependency of the domain — `docs/adr/0032-logger-slog-bridge.md`
- ADR 0033 — A rule the SDK cannot check is a suggestion; enforcement runs at build time, over source — `docs/adr/0033-consumer-rule-enforcement.md`
- ADR 0034 — the HCL quarantine is right, its stated mechanism is not: x/sys is introduced, never downgraded — `docs/adr/0034-hcl-quarantine-rationale-corrected.md`
- ADR 0035 — PP-range ownership is enforced by an audit the audited code cannot edit — `docs/adr/0035-pp-range-ownership-enforcement.md`
- ADR 0036 — form-urlencoded: repetition is the only array syntax, and the round-trip says so — `docs/adr/0036-sdk-codec-form-urlencoded.md`
- ADR 0037 — multipart/form-data: the boundary is not in the body, so the codec grows an extension interface — `docs/adr/0037-sdk-codec-multipart.md`
- ADR 0038 — NanoID, KSUID, TypeID: a Scheme that is deliberately not in the registry — `docs/adr/0038-id-schemes-and-the-unregistered-typeid.md`
- ADR 0039 — a published port is extended by a sibling interface, never by widening — `docs/adr/0039-extending-a-published-port-without-breaking-it.md`
- ADR 0040 — a published data shape may still change, and v0 is the only reason — `docs/adr/0040-changing-a-published-shape-while-v0.md`
- ADR 0041 — Time-driven execution domain (`scheduler`) — `docs/adr/0041-sdk-scheduler-domain.md`
- ADR 0042 — Security-token domain (`token`): the algorithm is bound by the constructor, not read from the token — `docs/adr/0042-sdk-token-domain.md`
- ADR 0043 — draining is announced to the handler, not imposed on it — `docs/adr/0043-drain-is-a-signal-not-a-cancellation.md`
- ADR 0044 — `metrics` adopts the OpenTelemetry DATA MODEL, and none of its code — `docs/adr/0044-metrics-adopts-the-otel-data-model.md`
- ADR 0045 — server-side session domain (`session`) — `docs/adr/0045-sdk-session-domain.md`
- ADR 0046 — Value-validation domain (`validation`) — `docs/adr/0046-sdk-validation-domain.md`
- ADR 0047 — WebSocket (RFC 6455), server side, in the stdlib — `docs/adr/0047-sdk-net-websocket.md`
- ADR 0048 — OTLP/JSON: the native wire, written from the document — `docs/adr/0048-sdk-metrics-otlp-json.md`
- ADR 0049 — `cache` becomes a domain, and gains the kernel primitive it needed to — `docs/adr/0049-cache-becomes-a-domain.md`
- ADR 0050 — ordered start/stop domain (`lifecycle`), and the two shutdown bugs that argue for it — `docs/adr/0050-sdk-lifecycle-domain.md`
- ADR 0051 — the `trace` domain: OpenTelemetry's third pillar, from the document — `docs/adr/0051-sdk-trace-domain.md`
- ADR 0052 — mutual-exclusion domain (`lock`): ownership, renewal and fencing decided out loud, and the guarantee that is NOT made — `docs/adr/0052-sdk-lock-domain.md`
- ADR 0053 — in-process event bus domain (`events`), and the line between it and `queue` — `docs/adr/0053-sdk-events-domain.md`
- ADR 0054 — asynchronous durable message queue domain (`queue`), the right-hand column of ADR 0053's frontier — `docs/adr/0054-sdk-queue-domain.md`
- ADR 0055 — relational-database domain (`sql`): transaction ownership, savepoints, and a migration lock that dies with its holder — `docs/adr/0055-sdk-sql-domain.md`
- ADR 0056 — filesystem domain (`vfs`), and exactly how far "atomic" reaches — `docs/adr/0056-sdk-vfs-domain.md`
- ADR 0057 — authorization domain (`authz`): abstention is a verdict, and there is no policy language — `docs/adr/0057-sdk-authz-domain.md`
- ADR 0058 — server-side rendering domain (`view`): a contract ON TOP of `html/template`, one trust type, and the XSS the SDK cannot see — `docs/adr/0058-sdk-view-domain.md`
- ADR 0059 — there is no `mapper` domain: a projection is a type, not a tag — `docs/adr/0059-no-mapper-domain.md`
- ADR 0060 — health: liveness and readiness are different questions, so they take different types — `docs/adr/0060-sdk-health-domain.md`
- ADR 0061 — Configuration schema: required keys, typed defaults, and a closed vocabulary — `docs/adr/0061-sdk-config-schema.md`
- ADR 0062 — a log line names the span it came from, and the bridge lives at the top layer — `docs/adr/0062-logger-trace-correlation.md`
- ADR 0063 — message-translation domain (`i18n`): a named CLDR subset, everything outside it refused BY NAME, and the guarantees that are NOT made — `docs/adr/0063-sdk-i18n-domain.md`
- ADR 0064 — electronic-mail domain (`mail`): composition is a mechanism, SMTP is a stdlib protocol, and a provider is neither — `docs/adr/0064-sdk-mail-domain.md`
- ADR 0065 — command-line domain (`cli`): what `flag` does not have, and the four things this adds instead of a framework — `docs/adr/0065-sdk-cli-domain.md`
- ADR 0066 — third-party compressors: zstd and s2, one library, one package, and a ceiling that cannot be omitted — `docs/adr/0066-third-party-compressors.md`
- ADR 0067 — an instrument gets a description, and it belongs to the NAME — `docs/adr/0067-metrics-instrument-description.md`
- ADR 0068 — the layer firewall is a checked graph, because Gazelle's visibility admits the whole repository — `docs/adr/0068-layer-firewall-is-a-checked-graph.md`
- ADR 0069 — behind a proxy that announces the scheme, the default origin rule refuses instead of guessing — `docs/adr/0069-websocket-origin-behind-a-proxy.md`
- ADR 0070 — the two keys the logger writes itself are reserved, and a caller's are renamed rather than dropped — `docs/adr/0070-logger-reserves-the-correlation-keys.md`
- ADR 0071 — a registry refuses a plug-in it cannot store, at the call that publishes it — `docs/adr/0071-a-registry-refuses-what-it-cannot-store.md`
- ADR 0072 — health bounds every wait it owns, including the two that belonged to somebody else — `docs/adr/0072-health-bounds-every-wait-it-owns.md`
- ADR 0073 — the session store's two waits can be abandoned, because a blocking flock cannot — `docs/adr/0073-session-waits-are-abandonable.md`
- ADR 0074 — a public alias points at the layer that OWNS the type, and a single engine's configuration is owned by that engine — `docs/adr/0074-what-a-public-alias-may-point-at.md`
- ADR 0075 — the SDK reads the cgroup cap that already bounds this process, not only the ones it writes for others — `docs/adr/0075-reading-the-cgroup-cap-that-already-bounds-us.md`
- ADR 0076 — what a branch changed is a value that can say it does not know, and running git against a repository you do not control is hardened — `docs/adr/0076-what-a-branch-changed-is-a-value-that-can-say-it-does-not-know.md`
- ADR 0077 — a self-update is an order of operations, and a product name is not part of it — `docs/adr/0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md`
- ADR 0078 — entitlement ships under third-party/ because proving key possession brings x/sys with it — `docs/adr/0078-entitlement-is-quarantined-because-ssh-brings-x-sys.md`
- ADR 0079 — the entitlement split: x/sys was never in the mechanism, only in the identity — `docs/adr/0079-the-entitlement-split-x-sys-was-never-in-the-mechanism.md`
- ADR 0080 — the gate decides, and performs nothing — `docs/adr/0080-the-gate-decides-and-performs-nothing.md`
- ADR 0081 — the Windows file lock: a different primitive, measured rather than recited, and the one rule it cannot run — `docs/adr/0081-the-windows-file-lock-is-a-different-primitive.md`
- ADR 0082 — the lock path is a file, never a link to one — `docs/adr/0082-the-lock-path-is-a-file-never-a-link-to-one.md`
- ADR 0083 — a path is a chain, and a held lock can lose its file — `docs/adr/0083-a-path-is-a-chain-and-a-held-lock-can-lose-its-file.md`
- ADR 0084 — the Windows lock directory has an answer, and it is not a mode — `docs/adr/0084-the-windows-lock-directory-has-an-answer-and-it-is-not-a-mode.md`
- ADR 0085 — Both halves of a release read the same range — `docs/adr/0085-both-halves-of-a-release-read-the-same-range.md`
- ADR 0086 — creating an entry is not replacing one, and Windows says so in two bits — `docs/adr/0086-creating-an-entry-is-not-replacing-one-and-windows-says-so-in-two-bits.md`
- ADR 0087 — the root a caller named is a spelling it did not choose, and a probe that did not answer is not an answer — `docs/adr/0087-the-root-a-caller-named-is-a-spelling-it-did-not-choose.md`
- ADR 0088 — a suite nothing runs is not a test suite, and a gate is a name CI says out loud — `docs/adr/0088-a-suite-nothing-runs-is-not-a-test-suite.md`
- ADR 0089 — A file that cannot cut a release cannot size one — `docs/adr/0089-a-file-that-cannot-cut-a-release-cannot-size-one.md`
- ADR 0090 — a port named in public must be implementable in public — `docs/adr/0090-a-port-named-in-public-must-be-implementable-in-public.md`
- ADR 0091 — a single trust anchor is a key with no way out — `docs/adr/0091-a-single-trust-anchor-is-a-key-with-no-way-out.md`
- ADR 0092 — a possession proof that cannot name the key it is about — `docs/adr/0092-a-possession-proof-that-cannot-name-the-key-it-is-about.md`
- ADR 0093 — a sweep takes the zombie, never the status — `docs/adr/0093-a-sweep-takes-the-zombie-never-the-status.md`
- ADR 0094 — a test compiles where its package does, and macOS runs every one — `docs/adr/0094-a-test-compiles-where-its-package-does.md`
- ADR 0095 — Windows runs every test and gates, and each of its nineteen failures was answered on its own terms — `docs/adr/0095-windows-runs-every-test-and-gates.md`
- ADR 0096 — a secret is a value no rendering writes down, a name with versions, and a key that rotates without breaking what it sealed — `docs/adr/0096-a-secret-is-a-value-no-rendering-writes-down.md`
- ADR 0097 — a load says where each value came from, and a secret field is loaded as written — `docs/adr/0097-config-provenance-and-secret-fields.md`
- ADR 0100 — a program reads what it was built from, and what it is doing, and asks git only about a working tree — `docs/adr/0100-a-program-reads-what-it-was-built-from-and-asks-git-only-about-a-working-tree.md`
- ADR 0101 — a secret shown is a secret replaced, and the bound is exact — `docs/adr/0101-a-secret-shown-is-a-secret-replaced-and-the-bound-is-exact.md`
- ADR 0102 — a document somebody else wrote is decoded one way, or not at all — `docs/adr/0102-a-document-somebody-else-wrote-is-decoded-one-way-or-not-at-all.md`
- ADR 0103 — a bucket per caller, one backoff curve, and a retry that waits on the clock it is given — `docs/adr/0103-a-bucket-per-caller-one-backoff-curve-and-a-retry-on-the-clock-it-is-given.md`
- ADR 0104 — an idle consumer sleeps until there may be work — `docs/adr/0104-an-idle-consumer-sleeps-until-there-may-be-work.md`
- ADR 0110 — a document store writes one entry per write and rests as one file — `docs/adr/0110-a-document-store-writes-one-entry-and-rests-as-one-file.md`
- ADR 0111 — a mail spool retries on a backoff, dead-letters with its last failure, and resends a delivered mail only after a crash — `docs/adr/0111-a-mail-spool-retries-on-a-backoff-and-resends-only-after-a-crash.md`
- ADR 0112 — a loop that must keep running is supervised, beside the lifecycle that starts it — `docs/adr/0112-a-loop-that-must-keep-running-is-supervised-beside-the-lifecycle.md`
- ADR 0120 — a state machine keeps an agenda, not a sweep — `docs/adr/0120-a-state-machine-keeps-an-agenda-not-a-sweep.md`
- ADR 0121 — a process reads its own profiles, and the attribution is the caller's — `docs/adr/0121-a-process-reads-its-own-profiles-and-the-attribution-is-the-callers.md`
- ADR 0130 — a file tree is served by name and never listed, and a failing accept waits — `docs/adr/0130-a-file-tree-is-served-by-name-and-a-failing-accept-waits.md`
- ADR 0131 — a readiness probe asks the loopback, and believes only 200 — `docs/adr/0131-a-readiness-probe-asks-the-loopback-and-believes-only-200.md`
- ADR 0132 — a floor a caller names is the floor applied — `docs/adr/0132-a-floor-a-caller-names-is-the-floor-applied.md`
- ADR 0133 — a Go type's wire shape is what encoding/json writes — `docs/adr/0133-a-go-types-wire-shape-is-what-encoding-json-writes.md`
- ADR 0134 — a program links the codecs it imports — `docs/adr/0134-a-program-links-the-codecs-it-imports.md`
- ADR 0135 — A release is sized by a label a maintainer set, never by text a merge composed — `docs/adr/0135-a-release-is-sized-by-a-label-a-maintainer-set.md`
- ADR 0136 — Every module is scanned for the vulnerabilities it reaches, and a reachable one blocks the merge — `docs/adr/0136-every-module-is-scanned-for-the-vulnerabilities-it-reaches.md`
- ADR 0137 — A lane that loops over modules reads the census, and names what it skips — `docs/adr/0137-a-lane-that-loops-over-modules-reads-the-census.md`
- ADR 0138 — A doc link resolves or it is not written, and a facade links the alias — `docs/adr/0138-a-doc-link-resolves-or-it-is-not-written.md`
- ADR 0139 — A document store over SQL joins the transaction its context carries — `docs/adr/0139-a-document-store-over-sql-joins-the-transaction-its-context-carries.md`
- ADR 0140 — SQLite's migration lock is the database file's write lock — `docs/adr/0140-sqlites-migration-lock-is-the-database-files-write-lock.md`
- ADR 0141 — a mail spool keeps an identifier its caller minted — `docs/adr/0141-a-mail-spool-keeps-an-identifier-its-caller-minted.md`
- ADR 0142 — one key per subject, under a root that rotates, and an erasure is the key's destruction — `docs/adr/0142-one-key-per-subject-under-a-rotating-root-and-an-erasure-destroys-it.md`
- ADR 0143 — a document keeps its versions in its own write — `docs/adr/0143-a-document-keeps-its-versions-in-its-own-write.md`
- ADR 0144 — illumos and Solaris supervise, on the word of their own kernels — `docs/adr/0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md`
- ADR 0147 — the framework is a module of the SDK, above `pkg`, and a product imports nothing else — `docs/adr/0147-the-framework-is-a-module-of-the-sdk-above-pkg.md`
- ADR 0148 — a private socket is gated by its directory, and the kernel names the peer where it can — `docs/adr/0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md`
- ADR 0149 — a product reports numbers on a port, and its exporter only emits — `docs/adr/0149-a-product-reports-numbers-on-a-port-and-an-exporter-only-emits.md`
- ADR 0150 — a release names its tag, its keys rotate, and a replacement answers before it stands — `docs/adr/0150-a-release-names-its-tag-keys-rotate-and-a-replacement-answers-before-it-stands.md`
- ADR 0151 — a message published in a transaction exists if and only if it commits — `docs/adr/0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md`
- ADR 0153 — the repository carries no devcontainer and no git hooks, and its guards run in CI — `docs/adr/0153-the-repository-carries-no-devcontainer-and-no-git-hooks.md`
- ADR 0154 — the SDK's principles are one charter, and an incident's rule lives with the code it hit — `docs/adr/0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md`
- ADR 0155 — every layer groups its packages by family, at the depth the families need, and an import path may move while v0 — `docs/adr/0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md`
- ADR 0156 — the public module links the standard library and nothing else — `docs/adr/0156-the-public-module-links-the-standard-library-and-nothing-else.md`
- ADR 0157 — one module per vendor, released with the SDK — `docs/adr/0157-one-module-per-vendor-released-with-the-sdk.md`
- ADR 0158 — distribution mechanisms are the framework's, not the SDK's — `docs/adr/0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md`
- ADR 0159 — the kernel holds what the domains were rewriting, and is published by nature — `docs/adr/0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md`
- ADR 0160 — every service has a core, and a code keeps its value when its declaration moves — `docs/adr/0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md`
- ADR 0161 — an untyped error fails the build — `docs/adr/0161-an-untyped-error-fails-the-build.md`
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
- Bazel adoption context — `.claude/contexts/bazel-9-go-sdk.md`
