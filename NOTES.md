# kitsunium/sdk — notes

The reference half of the root `CLAUDE.md` (ADR 0253): what an agent reads
when a task needs it rather than before every task. A section here keeps
the title it had there; the rules, the domain table and the `core/` block
the guards read stay in `CLAUDE.md`.

## Architecture at a glance

The tree below `internal/core/`, continued from the root `CLAUDE.md`:

```
└── service/       concrete implementations
                   observe (the family — ADR 0155; a directory, no Go code:
                           logger — + encoder, sink/{console,file,memory,
                             syslog}, middleware/{async,encwrite,failover,
                             multi,recover,route,sample,tee};
                           logger/writer — console, dbsink, file, journald,
                             levelgate, nettransport, otlpjson, rotfile;
                           logger/internal/logfile — the hardened open both
                             file sinks share;
                           metrics — in-memory meter + text/prometheus/
                             otlpjson exporters + the OTLP/HTTP emitter;
                           trace — tracer + samplers + recorder + batch sink + otlpjson
                             exporter + the OTLP/HTTP emitter + the
                             server/client HTTP middlewares + the W3C
                             traceparent/tracestate reader and writer
                             (moved from the core — ADR 0160 §4);
                           profiling — CPU/heap capture + a stdlib pprof
                             decoder + fold onto owners + goroutine dumps,
                             grouped;
                           internal/otlp — the OTLP/HTTP + JSON transport
                             metrics and trace share)
                   crypto (the schemes by role — ADR 0155:
                           aead/{aesgcm, streamaead}, agree/{x25519},
                           hash/{stdhash}, kdf/{hkdfsha256, keytree},
                           key/{jwk, keyenvelope}, mac/{hmacsha2},
                           password/{commonpw, pbkdf2pw},
                           sign/{ecdsasig, ed25519sig})
                   security (the family — ADR 0155; a directory, no Go code:
                           authz  — RBAC + ABAC + deny-overrides + Check;
                           redact — display redaction: names, declared
                             fields, URL credentials, within an exact
                             byte bound;
                           secret — memory, environment and sealed file
                             stores, keyring over versions, rotator; subject
                             keys under a rotating root, destroyed to erase;
                           session — memory store, file store (its Dir
                             audited with pathchain, then held as an
                             os.Root), AEAD cookie sealer;
                           token  — JWS compact + PASETO v4.public)
                   data   (the family — ADR 0155; a directory, no Go code:
                           cache — tagged memory store + L1/L2 chain;
                           codec — asn1, baseenc, bson, cbor, csv,
                             flatbuffers, form, json, msgpack, multipart,
                             ndjson, pem, tlv, toml, xml, yaml; + strictjson,
                             a decoder and not a Format, its request-body
                             form in strictjson/httpbody; + jsonshape, a
                             type's wire shape, not a Format; + jsonpatch,
                             two documents' difference, not a Format; + jcs,
                             RFC 8785 canonical JSON, not a Format; +
                             jsonschema, a JSON Schema 2020-12 validator;
                           docstore — typed JSON documents + unique/multi
                             indexes; one overlay entry per write, one
                             snapshot at rest; + the same store over SQL,
                             joined to the context's transaction — ADR 0139;
                             + a document's versions, in its own write —
                             ADR 0143; + its trash, a tombstone that keeps
                             them — ADR 0143 amended for ADR 0178;
                           queue — file, SQL and memory brokers + Consume
                             loop waiting on the Waker sibling; the SQL
                             broker joins the context's transaction —
                             ADR 0151;
                           stream — the engines of the partitioned log:
                             memory + the conformance suite every engine
                             runs + file over kernel/fs/seglog + SQL joined
                             to the context's transaction + Subscribe,
                             a worker per partition — ADR 0231;
                           sql — transaction manager + savepoints + pool
                             policy + Join/Defer siblings + migration
                             runner, SQLite's on the database file's write
                             lock — ADR 0140;
                           transform — the stdlib gzip, raw DEFLATE and
                             zlib compressors;
                           vfs — os.Root-confined FS + memory FS + atomic
                             publish)
                   proc   (the family — ADR 0155; a directory, no Go code:
                           buildinfo, cgroup, childwait, exec, memlimit,
                           reaper, rlimit, self, signal — process supervision,
                           ADR 0016; internal/rlim — the syscall.Rlimit
                           constructor exec and rlimit share;
                           internal/bsdabi — the BSD and darwin process
                           records exec reads off Linux, decoded from bytes;
                           systemd/{listen, notify} — socket activation and
                           sd_notify; ipc — a private socket between
                           processes of one machine, the engines behind
                           core/proc/ipc's Listener and Dialer ports: the
                           directory gates, the path above it audited with
                           pathchain (PATH_UNSAFE), SO_PEERCRED on Linux, a
                           named pipe with its own DACL on Windows —
                           ADR 0148, ADR 0160; ipc/rpc — a framed,
                           bounded, codec-typed call over that socket:
                           frames refused on their length, MaxInFlight
                           calls, budgets and cancel frames — ADR 0218;
                           service — one service's life cycle as
                           systemd.service(5) runs it, one actor over a
                           pure reducer — ADR 0214; bpf — the cgroup BPF
                           programs' engine: the one backend a vendor
                           module registers at import, NOT_LINKED without
                           it — ADR 0215)
                   net    (the family — ADR 0155; a directory, no Go code:
                           client, server, sse, static, tlsid, websocket —
                           one engine each over the one contract core/net;
                           the WebSocket and SSE wire formats are the
                           engines' own — ADR 0160 §4)
                   app    (the family — ADR 0155; a directory, no Go code:
                           config — env, file and fs.FS sources + layered
                             decode + schema + origins + poll watcher;
                           cli — resolution loop + generated help + config
                             seam;
                           i18n — CLDR plural table + catalogue + negotiator
                             + printer + the BCP 47 tag parser and pattern
                             compiler (ADR 0160 §4);
                           validation — constraints + combinators +
                             struct-tag plan;
                           view — html/template engine + trust scan +
                             parse-once;
                           events — synchronous priority-ordered bus +
                             On[E];
                           scheduler — cron parser + fixed interval + engine;
                           statemachine — declarations + per-entity
                             transitions + an agenda heap the loop sleeps on;
                           resilience — retry, circuit breaker, rate limit +
                             keyed, bulkhead (+ the one that queues), timeout,
                             fallback, hedging, adaptive throttle; the
                             backoff curve, an alias of kernel/backoff's;
                           lifecycle — ordered engine + per-component stop
                             budget + opt-in signal/sd_notify Run + the
                             supervisor;
                           health — check registry + per-check timeouts +
                             drain latch + HTTP handler + the loopback Ask;
                           consensus — the Raft engine (step function in
                             raft/, wire layouts in wire/, state machines
                             in machine/), segmented
                             file log, snapshot store, data directory and
                             offline operations, mTLS transport, and
                             multi/, a packer of many commands into one
                             entry — ADR 0152;
                           lock — in-process leases + file locker over
                             flock(2) or LockFileEx + keepalive;
                           id — uuidv4, uuidv7, ulid, snowflake, nanoid,
                             ksuid, typeid;
                           mail — the guards (Validate, the injection gate,
                             Envelope — moved from the core, ADR 0160) +
                             MIME composition + SMTP + memory and capture
                             doubles; spool/ — the durable outbox;
                           forward — store and forward: a batch kept in a
                             queue, bounded, retried, dead-lettered,
                             counted, + the trace/metrics adapters —
                             ADR 0185)
pkg/
└── v1/            stable public API (type aliases + ergonomic helpers)
    ├── clock/     (the time port: Clock/Waiter/Timed + System + ManualClock + CoarseClock — ADR 0090)
    ├── concur/    (a family directory, no Go code — ADR 0159 §4: group/, singleflight/,
    │                 worker/, batcher/, snapshot/, recycler/, pool/ — pure aliases
    │                 of the kernel's concurrency primitives; pool/ — ADR 0216)
    ├── collections/ (a family directory, no Go code — ADR 0159 §4: heap/, ring/ —
    │                 pure aliases of the kernel's containers, ring single- and
    │                 multi-producer — ADR 0216)
    ├── observe/   (a family directory, no Go code — ADR 0155:
    │                 logger/ — + ldflags-injected Version, + writer/, + slogbridge/;
    │                 metrics/ — the OTel data model, zero OTel imports — ADR 0044;
    │                 trace/ — W3C Trace Context + the OTel span model — ADR 0051;
    │                 profiling/ — the process's CPU, heap and goroutines, folded
    │                   onto your owners — ADR 0121)
    ├── errs/      (construction + introspection: New, Wrap, CodeOf, …)
    ├── data/      (a family directory, no Go code — ADR 0155:
    │                 codec/ — the aggregate of its sixteen per-format packages,
    │                   one format each with its codes — asn1/, baseenc/, bson/,
    │                   cbor/, csv/, flatbuffers/, form/, json/, msgpack/,
    │                   multipart/, ndjson/, pem/, tlv/, toml/, xml/, yaml/ —
    │                   ADR 0134; bson/ also names BSON's value types;
    │                   + strictjson/ — one document read one way — ADR 0102,
    │                   its request-body form in strictjson/httpbody/;
    │                   + jsonshape/ — a type's wire shape — ADR 0133;
    │                   + jsonpatch/ — two documents' difference — ADR 0143;
    │                   + jcs/ — RFC 8785 canonical JSON — ADR 0102 (2026-11-03);
    │                   + jsonschema/ — a JSON Schema 2020-12 validator — ADR 0210;
    │                 transform/ — compression without the codec package, the
    │                   caller's ceiling honoured — ADR 0014;
    │                 sql/ — ports over database/sql, no driver, no ORM — ADR 0055;
    │                   Joiner/Deferrer/Aborter — ADR 0139; SQLite migrations — ADR 0140;
    │                 docstore/ — typed JSON documents, indexes, one entry per write —
    │                   ADR 0110; the same store over SQL, OpenSQL — ADR 0139;
    │                   a document's versions, in its own write — ADR 0143;
    │                   its trash, Trash/Restore/Purge — ADR 0143 amended (ADR 0178);
    │                 queue/ — durable at-least-once broker, no lock — ADR 0054;
    │                 stream/ — a partitioned log every group reads at its own
    │                   pace, by offset — ADR 0231;
    │                 cache/ — the ADR 0025 primitive AND the ADR 0049 domain,
    │                   side by side;
    │                 vfs/ — io/fs reading unchanged + atomic publication — ADR 0056;
    │                 semver/ — SemVer precedence + Go pseudo-versions on the
    │                   standard library — ADR 0156 §4, ADR 0159 §4)
    ├── crypto/    (AEAD and keys at the family's root; its scheme facades are
    │                 its children — a child never links its parent, ADR 0155:
    │                 agree/, hash/, kdf/, mac/, sign/, and password/ — IsCommon
    │                 since ADR 0143)
    ├── security/  (a family directory, no Go code — ADR 0155:
    │                 authz/ — RBAC + ABAC, no DSL, abstention is the zero — ADR 0057;
    │                 redact/ — secrets replaced for display, exact bound — ADR 0101;
    │                   the Redactor a port a test can double — ADR 0160;
    │                 secret/ — a Value no rendering writes down, versioned stores,
    │                   keyring, rotator — ADR 0096; one key per subject, destroyed
    │                   to erase — ADR 0142;
    │                 session/ — memory + file Store, AEAD Sealer, Regenerate — ADR 0045;
    │                 token/ — JWT over JWS compact + PASETO v4.public — ADR 0042)
    ├── net/       (a family directory, no Go code — ADR 0155:
    │                 server/ — the inbound engine, its drain announced — ADR 0029/0043;
    │                 client/ — the guarded outbound HTTP client, posture enforced by
    │                   the transport — ADR 0029;
    │                 tlsid/ — TLS and mutual-TLS identities, shared by server and
    │                   client — ADR 0029;
    │                 sse/ — ADR 0029/0043; websocket/ — ADR 0047;
    │                 static/ — a file tree served by name — ADR 0130)
    ├── proc/      (the capability preflight at the family's root — ADR 0016, ADR 0144;
    │                 its facades are its children — a child never links its parent,
    │                 ADR 0155: process/ — spawn, wait, stop, and the process itself,
    │                 Self and Build — ADR 0100; buildinfo/ — Build alone, without
    │                 runtime/metrics or the spawn engine — ADR 0172;
    │                 signal/, reaper/, rlimit/, cgroup/;
    │                 memlimit/ — the cap already bounding this process — ADR 0075;
    │                 systemd/{notify, listen}/ — sd_notify and socket activation;
    │                 ipc/ — a private socket, the peer the kernel names — ADR 0148;
    │                   Listener/Dialer ports a test can double — ADR 0160;
    │                 ipc/rpc/ — a framed, bounded call over it — ADR 0218;
    │                 service/ — one service's life cycle, systemd.service(5)
    │                   as typed values — ADR 0214;
    │                 bpf/ — device, IP and bind policies attached to a
    │                   cgroup, the backend linked by importing
    │                   third-party/ebpf/cgroup, NOT_LINKED otherwise — ADR 0215;
    │                 dbus/ — a message bus client on the standard library:
    │                   a name's owner watched, a name owned — ADR 0214 (S0e))
    └── app/       (a family directory, no Go code — ADR 0155:
                      config/ — env, file and fs.FS layering, schema, origins,
                        poll watch — ADR 0028, ADR 0061, ADR 0097;
                      cli/ — flag + sub-commands + generated help + typed exit —
                        ADR 0065;
                      i18n/ — CLDR plurals over a named 13-language subset —
                        ADR 0063;
                      validation/ — Constraint / Violation / Report + the
                        struct-tag front end — ADR 0046;
                      view/ — html/template, one trust type, parse once — ADR 0058;
                      events/ — in-process synchronous bus — ADR 0053; NOT a queue;
                      scheduler/ — Parse/ParseInLocation/Every + the engine —
                        ADR 0041;
                      statemachine/ — entities moved by events, timers,
                        deadlines, guards; an agenda, not a sweep — ADR 0120;
                      resilience/ — retry, breaker, rate limit + keyed, bulkhead,
                        timeout, fallback, hedging — ADR 0026, ADR 0103; waits
                        bounded, three jitters, adaptive throttle — ADR 0184;
                      lifecycle/ — ordered start, reverse stop, per-component
                        budget — ADR 0050; the supervisor — ADR 0112;
                      health/ — startup, readiness and liveness — three
                        questions, three types — ADR 0060; Ask, the loopback
                        readiness probe — ADR 0131;
                      lock/ — Locker/Lease/Deadliner + memory & file lockers —
                        ADR 0052, ADR 0081, ADR 0082, ADR 0083;
                      id/ — UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID —
                        ADR 0024, ADR 0038;
                      mail/ — compose + Transport, injection refused — ADR 0064;
                      mail/spool/ — the durable outbox — ADR 0111, ADR 0141;
                      forward/ — store and forward for an export, the
                        trace and metrics adapters — ADR 0185)
third-party/       opt-in vendor integrations, one Go module per vendor
                   (ADR 0157) — see third-party/CLAUDE.md:
                   aws (writer/{cloudwatch,s3}), codec/hcl, codec/protobuf,
                   codec/yaml (yaml.v3 as the opt-in "yaml-full" Format,
                     beside the native "yaml" subset — ADR 0156),
                   db/writer/{clickhouse,mysql,redis},
                   ebpf (cgroup — the cgroup BPF backend over cilium/ebpf,
                     its programs built with asm — ADR 0215),
                   transform (zstd + s2 — ADR 0066),
                   x-crypto (argon2id, xchacha);
                   the SQL mechanisms' suites on real engines moved to
                     e2e/integration/sql (ADR 0139/0140, ADR 0157)
framework/         the layer above pkg/v1, packages of the root module — ADR 0147,
                   ADR 0162
                   model (the graph types + the ID grammar, Version 6),
                   app (the runtime a product imports, named after what
                     a product builds and never after the tool — ADR 0180;
                     beneath it the opt-in subsystems, which a product
                     imports and app never — ADR 0179 step 10 —:
                     config/{toml,yaml}, server,
                     studio — with the framework's languages after English,
                     ADR 0179 step 8 —, store — the stores and databases, ADR
                     0179 —, queue — the queues, step 4 —, workflow
                     — the workflows, step 5 —, mail — the mailers,
                     step 6 —, jobs — the jobs, step 7 — and otlp —
                     the product's OTLP export over app/forward, issue
                     #483 —, stream — a streamed topic's log over
                     data/stream, issue #826 —, storetest, the stores' conformance suite, and
                     apptest, a product's tests on disk — ADR 0175;
                     its engine internal/app, + plug, serverengine,
                     studioengine, storeengine, queueengine, workflowengine,
                     mailengine, jobengine, otlpengine, streamengine), telemetry (the
                     telemetry port + exporter — ADR 0149, the tool's
                     channel, apart from the OTLP export),
                   entitlement, selfupdate, gate, git (the distribution
                     mechanisms the SDK library shipped until ADR 0158:
                     signed roster -> grant; signature THEN digest THEN
                     disk; may this invocation run?; what a branch
                     changed and what a working tree is at), their
                     contracts and engines in internal/{core,service}/<domain>,
                   connectors/{postgres,mysql,sqlite} (one driver, one module each),
                   connectors/ssh (entitlement's ssh Identity + enrolment,
                     a module of its own — ADR 0079, ADR 0158)
```

`baseenc` is ONE codec registering nine base-N Formats
(base16/32/45/58/62/64/64url, hex, ascii85) through the same registry as every
other codec —
`internal/service/data/codec/baseenc`, published like the other fifteen by its
per-format facade `pkg/v1/data/codec/baseenc` (ADR 0134). There is no
byte-level base-N API: `codec.Marshal("base64", v)` is the one verb.

## Layout

```
sdk/
├── internal/              see internal/CLAUDE.md
├── pkg/v1/                see pkg/CLAUDE.md + pkg/v1/CLAUDE.md
├── third-party/           opt-in vendor integrations, one Go module per vendor — see third-party/CLAUDE.md (ADR 0157)
├── framework/             the framework above pkg/v1, packages of the root module — see framework/CLAUDE.md (ADR 0147)
├── design/                the SDK's design, the law of its exported surface (ADR 0163): `sdk.yaml` (layers, families, platforms, codes and their ranges, budgets — an inline budget deciding a forwarder's form, ADR 0166 —, exceptions) and one file per domain or package group declaring every exported symbol, its doc comment (`docs: design`, ADR 0167), its error codes and sentinels (ADR 0164) and `pkg/v1`'s re-exports (`facade:`, ADR 0166) included — kit's format, edited first, read by kit alone; the SDK's CI hashes its bytes; `design/behaviour/` holds the behaviour models derived from the code (`make models`), never edited, outside the design's canonical form — no generated file's digest reads them
├── docs/                  ADRs, and docs/api — the exported API, written from the code by `make api` — see docs/CLAUDE.md
├── .github/               CI workflows — bazel-ci.yml is the SDK lane
├── go.work, go.mod        workspace (read by Bazel via from_file) + the root module, `github.com/kitsunium/sdk`: the framework (ADR 0171); internal/ and each pkg/v1/<family> carry their own go.mod
├── MODULE.bazel           Bzlmod entry point (rules_go 0.60.0 + gazelle 0.50.0 + go_sdk 1.27.4 + go_deps)
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
├── tools/genindex/        docs/api (written and checked) + the doc-link check — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── tools/netgate/         the network plan's merge gates: paired bench rounds, each threshold held on benchstat's interval of their ratios (ADR 0190) (net-kernel-gate.yml) — auxiliary module, OUTSIDE go.work (GOWORK=off)
├── .golangci.yml          code-quality second-opinion linters (the layer firewall is scripts/check-layer-deps.sh — ADR 0068)
├── AGENTS.md              agent specs (not SDK)
└── README.md              the SDK quickstart (packages, install, verification)
                           — per-package docs live next to their code
```
