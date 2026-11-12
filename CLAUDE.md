<!-- updated: 2026-10-25T20:00:00Z -->
# kitsunium/sdk

## Purpose

Go SDK providing a normed, performant toolbox for downstream applications: the mechanisms a service is made of — typed errors, observability, codecs, cryptography, storage, networking, process supervision and the application mechanisms above them — built on the standard library (ADR 0156) and published as one stable surface, `pkg/v1`. The kernel's generic primitives build `internal/core`'s ports, values and error codes, the core builds `internal/service`'s engines, a complete service is exported through `pkg/v1`, and the framework (ADR 0147) sits above `pkg/v1`. Every domain follows the principles of the charter (ADR 0154), and ADR 0155 groups the tree into the families the table names. The whole exported surface is designed first: `design/` declares every exported symbol with its inputs and outputs, and the SDK's CI holds the code to it (ADR 0163). One line per domain below; what each ADR decided is digested in `docs/adr/CLAUDE.md`, and every package documents itself in its own `CLAUDE.md`.

| Domain | Family (ADR 0155) | What it is | ADRs |
|---|---|---|---|
| `errs` | root | Typed errors: `Define` / `Wrap`, dotted-quad `MM.LL.PP.SS` codes, a wire-safe `Public` and a log-only `Private`, origin wins on wrap | 0002, 0005, 0006, 0019, 0020, 0035, 0160, 0161 |
| `clock` | root | The time port — `Clock`, `Waiter`, `Timed`, `System`, `ManualClock`; every SDK wait runs on an injected clock | 0039, 0090 |
| `crypto` | crypto | AEAD `Seal` / `Open` with a hidden nonce at the family's root, and as its children hashes, MACs, KDFs, signatures, key agreement, password hashing (with `IsCommon`) and JWK/JWKS | 0013, 0014, 0143 |
| `secret` | security | A `Value` no rendering writes down; versioned stores (memory, environment, sealed file), a keyring and a rotator; one key per subject, destroyed to erase | 0096, 0142 |
| `redact` | security | Display redaction of values, JSON, text and log attributes — names, declared fields, URL credentials — within an exact byte bound | 0101 |
| `token` | security | JWT over JWS Compact and PASETO v4.public; the algorithm is bound by the constructor, never read from the token | 0042 |
| `session` | security | Server-side sessions: a self-redacting 256-bit identifier, memory and file stores (the file store's directory audited for a planted link, then held, so nothing is read or locked through one), absolute and sliding expiry, `Regenerate` the only login | 0045, 0073 |
| `authz` | security | RBAC and ABAC with no policy language; abstention is the zero verdict, deny-overrides the one combiner | 0057 |
| `net` | net | TLS/mTLS identities, the guarded outbound client, the server engine and its drain signal — an address another process holds refused at Start, shared only on `ShareAddress` —, SSE, WebSocket (RFC 6455) and a static file tree; an opt-in plaintext loop engine (io_uring, epoll, kqueue on darwin and the BSDs on request, portable) entered by `Group.HandleLoop` (`Shards(0)` one `SO_REUSEPORT` listener per loop; a loop pinned to a CPU, `SO_INCOMING_CPU`, `TCP_NOTSENT_LOWAT`; a slab budget derived from the cgroup allowance and given back when idle), the engine chosen by the handler's type and every group option judged by its kind, the operator's switches that only lower it (`KITSUNIUM_NET_ENGINE`, `KITSUNIUM_NET_DISABLE`) and a `State` that lists what the server runs without, and its restricted HTTP/1.1 profile, `net/http1`, with the links of a chain — routing, Bearer, response fields, an access log — judging one canonical target, loop middlewares (`UseLoop`) checked to forward every callback, `LoopsAuto` and a drain that stops the routing first and serves the listen queue, and datagrams read with UDP GRO and replies batched with `sendmmsg` and GSO on request (`BatchReplies`), or served on the loop engine (`PacketGroup.HandleLoop`: io_uring's multishot RECVMSG, the batched reader as its epoll lane), and an HTTP/2 codec on bytes (`net/http2`), the frame layer, HPACK and one connection's state machine, the loop's sibling profile (ADR 0221); a connection's copies reaching the socket's own, a wildcard datagram socket naming each datagram's destination and replying from it, the PROXY protocol of a trusted balancer, gzip response compression with static sidecars, datagram shards steered by a cBPF program, HTTP/2 beside HTTP/1.1 on that loop (`net/http2`, h2c by prior knowledge, a connection's first bytes choosing the version) | 0029, 0043, 0047, 0069, 0130, 0186, 0187, 0188, 0189, 0191, 0192, 0196, 0199, 0200, 0201, 0202, 0221, 0222 |
| `proc` | proc | Spawn and wait, signals, the reaper, rlimits, cgroups — nested, their controllers and cgroup v2 files written by the SDK alone, `cgroup.events` watched — and the cap already bounding the process, sd_notify and socket activation, what the program was built from; a child gets only the descriptors it is named, a wait is bounded, a signal goes through a pidfd or HANDLE, a child may die with its parent, run on a pseudo-terminal or under a scheduling class, or be sandboxed — namespaces, mounts, a new root, capabilities, Landlock and a seccomp filter applied before its program runs; one service's life cycle as systemd.service(5) runs it — Type=, Exec*=, the timeouts, the watchdog, Restart= and the start limiter — one actor over a pure reducer (`proc/service`), a forking daemon adopted from its PID file or its cgroup, the descriptors a service stores handed back to its next main process, a unit per accepted connection, the cgroup modes on a unit's own group — `KillMode=control-group` its default there, `mixed`, `ExitType=cgroup`, `Delegate=`, `OOMPolicy=`, and `Type=dbus` up on its bus name, through `proc/dbus` — a message bus client on the standard library; a process adopted from a PID file read safely, the whole sd_notify transport, a child told its own `LISTEN_PID`; a device, IP or bind policy attached to a cgroup as a BPF program, the backend a vendor module (`third-party/ebpf`) a program links by importing it | 0016, 0075, 0093, 0100, 0144, 0214, 0215, 0217, 0219 |
| `ipc` | proc | A private socket between processes of one machine: the directory gates it, the path above that directory is refused where another account could steer it, the kernel names the peer where it can | 0148, 0182 |
| `rpc` | proc | A call between two processes over the private socket: a codec-named hello, frames refused on their length before anything is allocated, `MaxInFlight` calls answered `OVERLOADED` past it, a budget on the server's clock and a cancel frame, an SDK error's code across | 0218 |
| `logger` | observe | Structured logger, one allocation per emit, trace-correlated; named writers (console, file, rotation, journald, database, network) and a `slog` bridge at the edge | 0012, 0015, 0030, 0032, 0062, 0070, 0132 |
| `metrics` | observe | The OpenTelemetry metrics data model from its specification, zero OTel imports; text, Prometheus and OTLP/JSON exporters | 0027, 0044, 0048, 0067, 0208 |
| `trace` | observe | The OTel span model and W3C Trace Context from their documents, sampled once at the root, exported as OTLP/JSON | 0051 |
| `profiling` | observe | The process's own CPU, heap and goroutine profiles, decoded with the standard library and folded onto owners the caller names | 0121 |
| `codec` | data | 24 wire formats behind one `Marshal` / `Unmarshal` dispatch with per-format facades, strict JSON decoding, a type's wire shape and JSON patch | 0003, 0021, 0022, 0023, 0036, 0037, 0102, 0133, 0134, 0143, 0156 |
| `transform` | data | Compression: stdlib gzip, raw DEFLATE and zlib, each at a level of the caller's; zstd — with a dictionary of the caller's — and s2 opt-in under `third-party/`, with a ceiling that cannot be omitted; whole buffers, or streams read under the caller's ceiling | 0014, 0066, 0213 |
| `sql` | data | Ports over `database/sql`, never an ORM or a driver: structural transaction ownership, savepoints, migrations under a lock that dies with its holder, checksummed when built from statements; a retrying root transactor, a statement budget, read-only transactions on a replica, a statement observer under the pool | 0055, 0139, 0140 |
| `docstore` | data | Typed keyed JSON documents with indexes, versions and a trash, replaced on the revision they were read at — one overlay entry per write through `vfs`, or tables of the caller's database | 0110, 0139, 0143, 0178, 0209 |
| `queue` | data | Durable at-least-once queue over a directory, a SQL table joined to the caller's transaction, or memory; retried — with a jitter on request — and dead-lettered with its cause's codes, its trail's included, a dead letter and a live message movable between brokers as they lie; a message held until one instant, a retry after the handler's delay, a publication once per key over a window; a message's age bounded, a backlog counted, purged, published and acknowledged in batches, dead letters paged, replayed and purged in bulk and kept for a retention, a handler that acknowledges in its own transaction, five priority levels served strictly | 0054, 0104, 0151, 0178, 0183 |
| `stream` | data | A durable, partitioned, offset-addressed log read by independent consumer groups, each record kept until its retention ends — the port, its siblings and its policy, the memory engine, the conformance suite every engine runs, a file engine over segments retained by age and bytes, a SQL engine joined to the caller's transaction, a subscriber per group committing its offset after the handler or in the handler's own transaction, and compaction by key with tombstones, offsets kept; published as `pkg/v1/data/stream` | 0231 |
| `cache` | data | The kernel LRU+TTL primitive and the domain above it: tag invalidation, in-process stampede protection, L1/L2 chaining; a Valkey far tier under `third-party/` | 0025, 0049, 0211 |
| `vfs` | data | `io/fs` reading unchanged and atomic publication, with how far "atomic" reaches stated, a batch under one directory flush; an S3 filesystem under `third-party/` | 0056, 0211 |
| `config` | app | Environment, file and `fs.FS` layering, typed decode, a schema (required keys, defaults, unknown keys refused), provenance, a poll watcher | 0028, 0061, 0097 |
| `cli` | app | Command lines on the stdlib `flag`: sub-commands, generated help, a typed exit status, a seam to `config` | 0065 |
| `i18n` | app | Message translation over a named 13-language CLDR subset; every other language refused by name | 0063 |
| `validation` | app | A `Constraint` port, located violations and the engine composing them; a message never names the value | 0046 |
| `view` | app | Server-side rendering on `html/template`: one trust type, templates parsed once, `text/template` refused | 0058 |
| `events` | app | An in-process synchronous event bus keyed on Go types — not a queue | 0053 |
| `scheduler` | app | Five-field POSIX cron and fixed intervals, with DST, missed deadlines and overlap decided — skip, allow or replace — and a starting deadline past which a late fire expires | 0041, 0184 |
| `statemachine` | app | Entities moved by events, timers, deadlines and guards over the caller's store, on an agenda rather than a sweep | 0120 |
| `resilience` | app | Retry, circuit breaker, keyed rate limit, bulkhead, timeout, fallback, hedging, and the published backoff curve; a retry budget shared by retry and hedge, and the delay a failure imposes (`RetryAfter`); a breaker that opens on a rate of failures or slow calls over a sliding window, and says where it stands (`BreakerState`); a bulkhead and a limiter that wait a bounded time, three jitters, and the SRE book's adaptive client-side throttle | 0026, 0031, 0103, 0184 |
| `lifecycle` | app | Ordered start and reverse stop with a per-component budget, and the supervisor that restarts a loop — and gives up past an Erlang restart intensity | 0050, 0112, 0184 |
| `health` | app | Startup, readiness and liveness as three types, every wait bounded, and the loopback readiness probe | 0060, 0072, 0131 |
| `consensus` | app | A write is committed when a majority holds it on disk: a Raft with PreVote and CheckQuorum always on, snapshots, single-server membership through learners, a segmented log that classifies every hole before it repairs one, an mTLS transport pinned by SPKI | 0152, 0174 |
| `lock` | app | Named exclusive leases over one process or one machine, with fencing tokens and the guarantee that is not made said out loud | 0052, 0081, 0082, 0083, 0084, 0086 |
| `id` | app | UUIDv4/v7, ULID, snowflake, NanoID, KSUID and TypeID | 0024, 0038 |
| `mail` | app | MIME composition and SMTP with header injection refused, and a durable spool | 0064, 0111, 0141 |
| `forward` | app | Store and forward: an export kept in a durable queue while its collector is down, bounded in batches and bytes, retried on the collector's Retry-After or a backoff, dead-lettered when refused, counted; the trace and metrics adapters and their `DocumentExporter` | 0185 |
| `selfupdate` | framework (ADR 0158) | A signed release verified before its digest, then an atomic replacement; no key, no install | 0077, 0150 |
| `entitlement` | framework (ADR 0158) | A vendor-signed roster read into a grant, with an offline cache, an anti-rollback ratchet and a CI seat | 0078, 0079, 0091, 0092 |
| `gate` | framework (ADR 0158) | Whether an invocation is subject to the check: a policy value and a decision that performs nothing | 0080 |
| `git` | framework (ADR 0158) | What a branch changed and what a working tree is at, through hardened git invocations; it degrades, never empties | 0076, 0087, 0100 |

**Repository**: `github.com/kitsunium/sdk` · **Modules**: the root (the framework), `…/internal`, one `…/pkg/v1/<family>` per family, and the vendor modules (ADR 0171) · **Go**: 1.27.4 (pinned in `MODULE.bazel`)

## Architecture at a glance

```
internal/
├── kernel/        stdlib-only AND generic primitives, by family (ADR 0155)
│                  backoff, clock, errs, jsonenc, plugin, semver, swar,
│                  concur/{batcher, buffer, commit, group, pool,
│                          recycler, sema, singleflight, snapshot,
│                          worker},
│                  collections/{cache, heap, ring},
│                  fs/{flock, pathchain, seglog, winacl}
├── core/          domain interfaces + domain values — each package by its path
│                  crypto, crypto/key/jwk, net, net/http2,
│                  proc, proc/bpf, proc/dbus, proc/ipc, proc/ipc/rpc, proc/service,
│                  app/{cli, config, consensus, events, forward, health, i18n, id,
│                       lifecycle, lock, mail, mail/spool, resilience,
│                       scheduler, statemachine, validation, view},
│                  data/{cache, codec, codec/scratch,
│                        codec/{asn1, baseenc, bson, cbor, csv, flatbuffers,
│                               form, jcs, json, jsonpatch, jsonschema,
│                               msgpack, multipart, ndjson, pem,
│                               strictjson, tlv, toml, xml, yaml},
│                        docstore, queue, sql, stream, transform, vfs},
│                  observe/{logger, logger/{level, writer},
│                           logger/middleware/{async, encwrite, failover,
│                           multi, recover, route, sample, tee},
│                           logger/sink/{console, file, syslog},
│                           logger/writer/{journald, nettransport, otlpjson, rotfile},
│                           metrics, otel, profiling, trace},
│                  security/{authz, redact, secret, session, token}
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
                             raft/, wire layouts in wire/), segmented
                             file log, snapshot store, data directory and
                             offline operations, mTLS transport — ADR 0152;
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

- **A Go module per pkg family, one shared `internal` module, and the root** (ADR 0171, amending ADR 0162): `github.com/kitsunium/sdk/pkg/v1/<family>` for each of the eleven families — `app`, `clock`, `collections`, `concur`, `crypto`, `data`, `errs`, `net`, `observe`, `proc`, `security` —, so a consumer locks each family at its own version and a fix in one is one `go get` that moves nothing else; `github.com/kitsunium/sdk/internal` for `internal/{kernel,core,service}`, one module because a domain's core and service halves are two subtrees, which Go's `internal/` rule keeps every importer outside the repository out of; and the root `github.com/kitsunium/sdk`, which holds the framework under `framework/` and nothing of `pkg/` or `internal/`. Import paths are unchanged (`…/pkg/v1/*`, `…/framework/*`), and none of them requires a module outside the standard library (ADR 0156). Beside them `go.work` names the thirteen modules that require a vendor, one vendor each, so a consumer of one integration takes that vendor's graph and no other's: the nine under `third-party/` — `aws`, `codec/hcl`, `codec/protobuf`, `codec/yaml`, `db/writer/{clickhouse,mysql,redis}`, `transform` and `x-crypto` (ADR 0157), each requiring `internal` — and the framework's four connectors — the three database engines `framework/connectors/{postgres,mysql,sqlite}`, one driver and one module each, and `framework/connectors/ssh`, entitlement's ssh `Identity` (ADR 0158). `design/sdk.yaml` declares all twenty-six and what each may require (`modules:`, kit's `modules` rule holds every go.mod, require directive and import to it, and the requirements form no cycle — the tests' six cycles were broken, ADR 0171 §7). Every go.mod requires each repository module of its build's closure through a relative `replace`, so `GOWORK=off go build ./...` works in it; the release drops them, pins versions and writes the floor on the root into `internal`'s and every family's (`scripts/release/cut-tags.sh`). Four **auxiliary** modules are deliberately kept OUT of `go.work`: `e2e/`, `tools/genindex/`, `tools/netgate/` and `tools/sdkguard/`. Bazel's `go_deps` extension reads `go.work` and cannot process extra modules, so adding any of them breaks the build; they carry `replace` directives (or need none) and are built with `GOWORK=off`. Counting `go.mod` files therefore yields thirty — that is not drift, it is the invariant — and `bash scripts/ci/go-modules.sh` prints them: it is the census every lane that loops over modules reads, so a module git tracks is built, vetted, tested on 32 bits and scanned without anybody editing a workflow, and none is skipped (ADR 0137). A pattern never crosses a module boundary — `GOWORK=off go build ./...` at the repository root builds the framework and none of the modules nested in it — so a check of everything loops over that census. See `e2e/CLAUDE.md` §Do NOT and `tools/CLAUDE.md` §Do NOT. A release tags each module `go.work` names that changed since its OWN last tag, at its OWN version — `vX.Y.Z` for the root, `pkg/v1/<family>/vX.Y.Z`, `internal/vX.Y.Z`, `<dir>/vX.Y.Z` for a vendor module —, sized by the labels of the merges that touched it, named by one `release-manifest.json` and one GitHub release; no module is ever tagged at or below the highest version the proxy lists for it, and nothing is cut while the repository variable `RELEASE_ENABLED` is not `true` — see `scripts/release/CLAUDE.md`. The root's v0.18.0 to v0.19.7 held `pkg/` and `internal/` and are retracted by its go.mod; the `internal/*`, `pkg/*` and `framework/*` tags of the releases before v0.18.0 are history; v0.18.0 cut `pkg/v0.18.0` and `framework/v0.18.0` once more, as tombstones, and a consumer still on the modules before ADR 0162 migrates with the one command it gives.
- Dependency direction is strictly top-down: kernel → core → service → pkg/v1 → framework, with `third-party/` above all but the framework. It is enforced on the BUILD GRAPH by `scripts/check-layer-deps.sh` (in `make lint` and CI — ADR 0068), not by visibility: ADR 0004 put it on `package_group` + `visibility`, but Gazelle gives every package under `internal/` the visibility `//:__subpackages__`, which admits the whole repository, and a kernel package importing core was shown to build. A rogue import fails `make lint` and CI, naming the target it reached.
- Consumers import only `pkg/v1/*`; `internal/*` is blocked by Go's `internal/` firewall, which is also what keeps them out under Bazel.
- Build / test / lint go through **Bazel 9** — see ADR 0004. `go test ./...` still works locally for quick iteration but CI only runs `bazel`. Because the two build systems disagree about what is in scope, anything excluded from one MUST be covered by the other — see rule 12.

## Where things stand

- **The split release is published.** The first per-module release (ADR 0171) is on origin: the root at `v0.20.0`; `internal/v0.1.0` and the eleven families at `pkg/v1/<family>/v0.1.0`; the four connectors (`framework/connectors/{mysql,postgres,sqlite,ssh}`) and the nine vendor modules under `third-party/` at `v0.20.0`; and the tombstones `internal/{kernel,core,service}/v0.18.0`.
- **Automatic releases are on.** The repository variable `RELEASE_ENABLED` is `true`, so `sdk-release.yml` cuts after every green `SDK CI (Bazel)` run on `main`. The base now holds `internal/go.mod`, so the bootstrap hold no longer applies: each module continues its own last tag (`scripts/release/CLAUDE.md`).
- **ktn-linter runs in CI and blocks.** The `lint-ktn` job of `bazel-ci.yml` installs the vendor's release binary with `kodflow/ktn/setup` (pinned by commit) and proves the licence with the runner's OIDC token — `permissions: id-token: write` on that job only, no repository secret, no device seat — then runs `make lint-ktn-check`, which is in `scripts/ci-gates-check.sh`'s `GATES`; the required `bazel` job needs it and fails on its result. Never grant the id-token to `bazel`: it exports `ACTIONS_ID_TOKEN_REQUEST_URL`, which the entitlement suites read. A fork or Dependabot pull request gets no id-token and is skipped with a warning; any other run without one fails. The run is covered only while the vendor's signed roster lists this repository owner's numeric id (`kitsunium`, an organisation). See `scripts/CLAUDE.md` §ktn-linter in CI.

## How to work

| Goal | Flow |
|---|---|
| New feature or bug fix | `/plan "description"` → `/do` → `/git --commit` → `/git --merge` |
| Code review | `/review` |
| Linting | `make lint` (mod-tidy + gazelle drift + gofumpt -l + ktn-linter + alloc-lane coverage + audit coverage + domain-doc drift + core symmetry + platforms table + package docs + BENCH.md presence + error-code drift + layer firewall + `make guard` + `make doclinks` + `make api-check`) |
| Vulnerability scan | `make vuln-install && make vuln-check` — govulncheck over every module, fails on a reachable vulnerability (ADR 0136); online, so not part of `make lint` |
| Local test suite | `make build && make test` (build prep + race tests) |
| Allocation gates | `make test-alloc` — race-off pass; the ONLY lane that runs `//go:build !race` tests (targets in `tools/alloc-lane-targets.txt`, see rule 12) |
| Single-package test | `bazel test //<path>:<target>` (e.g. `bazel test //internal/kernel/errs:errs_test`) |
| Regenerate BUILD.bazel | `bazel run //:gazelle` after changing imports or `go.mod` |
| Coverage | `bazel coverage --combined_report=lcov //...` — LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| Release dry-run | `make release-dry-run` (computes the release — each module that changed since its own tag, at its own version — locally without pushing tags — see ADR 0007, ADR 0162, ADR 0171) |
| Change the exported API | **the design first** (ADR 0163): edit the domain's file under `design/` (`design/<family>/<domain>.yaml`, or the kernel, framework or vendor group's) → `kit gen` (the pins `gen_api*_test.go` of every designed symbol, the ports `gen_ports.go` of the interfaces it declares under `ports:`, the codes and sentinels `gen_codes.go` of its `codes:` and `sentinels:` — ADR 0164 —, and a `pkg/v1` package's aliases, re-exports and forwarders `gen_facade.go` of its `facade:` — ADR 0166, a forwarder with an inline budget written as a variable while the compiler, measured, does not inline it — and, since ADR 0167, every doc comment's slot and each package's `doc.go`; since ADR 0168, `gen_decls.go`: the types a design declares `decl: true`, the wrappers of the functions and methods that name an `impl:` — each, with its impl, measured to inline — and the `implements:` assertions, the bodies staying hand-written) → make the code follow → `make api` → `make docs-readme`. It needs kit, the owner's tool, which is not public: a change of the API made in the code alone fails `make api-check` (the pins' markers name the symbol), and a design edit without `kit gen` fails it on the digests. Never edit a generated file — `gen_api*_test.go`, `gen_ports.go`, `gen_codes.go`, `gen_ranges_test.go`, `gen_facade.go`, `gen_decls.go` — by hand |
| Prove the design regenerates the code | `make regen` — local only, it needs kit at the version `design/sdk.yaml` pins (`project.kit.version`) and refuses any other, and a tree with no tracked change and no untracked generated file: deletes every tracked file whose FIRST line is kit's generated header (never by name — every one is `gen_*.go`, ADR 0181, but the header decides) and clears kit's section of `tools/alloc-lane-targets.txt`, runs `kit gen` then `make api`, and fails on any `git diff` or any file it adds that git does not track (`scripts/regen.sh`). No CI lane runs it, because CI runs without kit; `make api-check` is CI's hold on the same files |
| Prove the SDK is rebuilt from the design alone | `make from-zero` — local only, like `make regen` (the pinned kit), but over uncommitted work and never in the working tree: in a temporary copy of every file git tracks or would track, it deletes every file carrying kit's generated header and kit's section of `tools/alloc-lane-targets.txt`, runs `kit gen -stubs` and requires every module of the census to compile (`go build` + `go vet`) from the design and the hand-written bodies — the stubs counted per package, 0 where every body exists —, then `kit gen` + `make api`, and fails on any file added, missing or changed against the working tree (`scripts/from-zero.sh`, ADR 0169) |
| Count how much of the SDK kit writes | `make kit-coverage` — per package, the exported declarations in files kit generated (`gen_decls.go`, `gen_facade.go`, `gen_codes.go`, `gen_ports.go`) against those written by hand, the total and the ten packages with the most by hand (`kit design coverage`, `scripts/kit-coverage.sh`, ADR 0169). A report, never a gate; local (the pinned kit) |
| Keep the behaviour models | after a change to a body, `make models` — the pinned kit's `kit design derive` writes again, under `design/behaviour/<the package's directory>/`, what each body does (`classes.yaml`, `sequences.yaml`, `workflows.yaml`), derived from the code and never edited (the Studio's ADR 0013; the bodies stay the code's, ADR 0168); commit them with the change. A new package is modelled by naming it once (`kit design derive ./<its directory>`); a pattern reaches one module only, so `./pkg/...` names nothing and a family is named by its own directory (`./pkg/v1/<family>/...`). `make models-check` and `kit check`'s `behaviour` rule fail on a model missing, not what the current code derives, or left over. Local (the pinned kit) |
| Regenerate docs/api | `make api` — after a doc edit, and after any change to the exported surface: `docs/api/<module>.json` for every module of `go.work`, every exported symbol read from the code with go/types on the twelve cells of `scripts/ci/platforms.sh` (`tools/genindex -write-api`; the format is `docs/api/schema.json`), then `docs/error-codes.yaml` from it (`make error-codes`) and gazelle, which gives new pin files their test targets. A doc edit is a design edit since ADR 0167: the doc in `design/`, then `kit gen`, then this, then `make docs-readme`; `make api-check`, in `make lint-check`, fails until it is run, and fails on a doc edited in the code alone (the pin markers' doc digests) |
| Regenerate READMEs | `make docs-readme` — `tools/genindex -write-readmes` writes the `README.md` of every package under `pkg/` and `framework/` that docs/api records (116 today, the four `framework/connectors/*` modules included) from the committed docs/api; nothing to install. `scripts/pre-commit/check-readme-drift.sh` (`make lint`, CI) checks them; see ADR 0167 |

Branch naming matches the conventional commit prefix: `feat/*`, `fix/*`, `refactor/*`, `chore/*`, `docs/*`.

## SDK-wide rules (non-negotiable)

1. **Kernel gate.** A kernel package MUST be stdlib-only AND generic (no domain vocabulary). `level` was moved OUT of kernel because it fails the second half — see ADR 0002 / the layer-placement audit in `.claude/contexts/sdk-layer-placement-audit.md`.
2. **Typed errors only.** Every error returned from SDK code goes through `errs.Define` or `errs.Wrap` (`internal/kernel/errs`). `fmt.Errorf` / `errors.New` are banned in production code — every non-`_test.go` file under `internal/`, `pkg/`, `third-party/` and `framework/`, with no exemption. `make guard` enforces it: `tools/sdkguard`'s SDK002 over that tree, failing `make lint` and CI's lint gate on either call and on a `//sdkguard:allow SDK002` directive (SDK002 stays a convention for consumers — ADR 0033). `errors.Is` / `As` / `AsType` / `Join` / `Unwrap` and `errors.ErrUnsupported` stay allowed.
3. **Dotted-quad error codes.** `Code` is a `uint32` laid out `MM.LL.PP.SS` (Major / Layer / Package / Serial) — see ADR 0005. Each package owns a `PP` slot; ADR 0005 §Registry + the ADR 0006 extension (logger v2 + ring) are the authoritative allocation table. Two AST audits enforce it: `registry_external_test.go` checks that no two `Define` calls resolve to the same **value**, and `registry_ownership_external_test.go` checks **range ownership** — one package per `MM.LL.PP`, against the `codeRangeOwners` table, deliberately independent of the constants it audits (ADR 0035). Since ADR 0164 both are the design's and kit writes them: each package's codes and sentinels are its `codes:` and `sentinels:` in its domain's design file, written into `gen_codes.go` with every comment, and the table is `design/sdk.yaml`'s `codes.ranges`, written into `internal/kernel/errs/gen_ranges_test.go` — a block authored apart from the codes, never derived from them, so the audit still compares two independent inputs. A code, a sentinel or its text changes in the design, then `kit gen`; a new range is allocated in `codes.ranges` in the same change that introduces its codes, and never by editing a generated file. Both audits only judge files that reach them as runfiles of `//:audit_sources`, so `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring codes is missing from that list — the gap ADR 0020 records having already hidden ~10 emitters. Where a code is declared is checked too (ADR 0160): every code is declared by an `internal/core` package, at the path of the engine that raises it, and `scripts/pre-commit/check-core-symmetry.sh` — `make lint` and CI — fails when a production file under `internal/service` declares one (an `errs.Define` under any import name, or a `Code` constant), when a service domain has no core package at the same path, or when a core package declares a code no engine at its path accounts for — a layer-3 range sits at its engine's path, and only a sub-contract beneath a domain may hold, without one, a range the core allocated (`observe/logger/level`). A code's VALUE never changes when its declaration moves: `LL` records the layer that allocated the range, not the directory that declares it, so an engine's range declared in the core keeps `0.3.PP.*` and the distribution domains' ranges kept theirs in the framework (ADR 0160 §3, ADR 0158) — `codes.ranges` keeps its ranges and re-points its owners, and `make error-codes` shows the same values under new paths. Match codes with `errs.HasCode(err, CodeX)` (walks `Unwrap() error` *and* `Unwrap() []error`) or `errors.Is(err, errs.NewPrefixMatcher(...))` for subnet-style routing.
4. **Public/Private split.** Every SDK error carries a wire-safe `Public` (string literal ≤120 runes, no newline) and a log-only `Private`. `err.Error()` renders `"[<code> <REASON>] <public>"` on the no-trail fast path; when the wrap trail is non-empty, ADR 0005 §Semantics extends the bracket header with `" <- "`-separated trail codes and an optional `" (truncated)"` marker — never Private, never Fields. Log-parser regex: `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`.
5. **No empty stub files / dirs.** If a file or directory only carries a placeholder, inline its content into an existing file or delete it.
6. **Origin wins on wrap.** When `errs.Wrap` receives an `*errs.Error` cause, it inherits the cause's Code/Reason/Public/Private. Wrappers can only add `Fields` (and extend the intrinsic wrap trail). To relabel, define a fresh sentinel.
7. **`Version` via build-time injection.** `pkg/v1/observe/logger.Version` is stamped at link time — under Bazel via `x_defs` + `--stamp` + `tools/workspace_status.sh` (`STABLE_VERSION`); under raw `go build` via `-ldflags "-X github.com/kitsunium/sdk/pkg/v1/observe/logger.Version=…"`. `FrameworkVersion()` returns `"dev"` when unset; every emitted log record carries `framework_version` automatically.
8. **Every package is documented.** The guard `scripts/pre-commit/check-pkg-docs.sh` — run by `make lint` and CI — fails when any `internal/*`, `pkg/v*/**` or `framework/**` directory containing Go production code is missing `CLAUDE.md` AND `README.md`. Public packages (`pkg/v*/**`) and every framework package require BOTH: `README.md` (consumer-facing — pkg.go.dev renders it; the model is `pkg/v1/errs/README.md`) and `CLAUDE.md` (agent-facing), because the two roles do not collapse. The `scripts/` and `docs/site/scripts/` trees are tooling, not library code, and the gate does not walk them.
9. **Every benchmark package ships its numbers.** The guard `scripts/pre-commit/check-bench-md.sh` — run by `make lint` and CI — fails when a directory contains `*_bench_test.go` but no sibling `BENCH.md`. The report is regenerated with `make bench`; it stamps machine, RAM, CPU, OS, Go toolchain, git SHA, and timestamp so cross-machine deltas can be evaluated honestly. A number is never generated: the benchmarks kit gen writes from a design budget's `bench:` (`gen_perf_test.go`, ADR 0165) have their ns/op read only as a benchstat delta against the base (`nsop: {maxDelta, vs: base}`), never as an absolute figure, and `make bench` remains the only writer of a BENCH.md.
10. **`pkg/v*/**/README.md` are generated, not hand-authored — and so are the framework's.** `tools/genindex -write-readmes` (`make docs-readme`) writes the `README.md` of every package under `pkg/` and `framework/` that docs/api records, from the committed docs/api (ADR 0167, amending ADR 0008): the package comment, then each exported symbol's declaration and doc — an alias with the declaration, field docs and methods of the type it names —, each value's code, and the examples of the package's tests. docs/api's docs are the design's, so a README changes with the design: edit the doc in `design/`, `kit gen`, `make api`, `make docs-readme`; `scripts/pre-commit/check-readme-drift.sh` — run by `make lint` and CI's `bazel` job — fails on a README that differs, is missing, or is stale. Maintainer rationale (Why-this-shape, layering, do-not lists) stays in `CLAUDE.md` — consumer-facing prose belongs in the doc comments, which are the design's.
11. **Docs travel with the code — always update them in the same change.** Documentation is part of the change, never a follow-up. Whenever you add/rename/remove an exported symbol, package, format, code range, capability, or convention, update every doc that describes it **in the same commit**: the package's `CLAUDE.md` (Purpose/Surface/Contents/Sentinels), the parent/layer `CLAUDE.md` tables (e.g. `internal/service/data/codec/CLAUDE.md` Streaming/Appender columns, `internal/core/CLAUDE.md` registry counts), the root `CLAUDE.md` domain list, and — for every designed symbol and package — its doc in `design/`, which `kit gen` writes into the code and genindex renders into `README.md` (rule 10, ADR 0167). A doc that names a symbol, count, file, or code that no longer matches the code is a defect: fix the doc or the code, never leave them divergent. When in doubt, grep the docs for the old name/number before committing. Two of this rule's failure modes are now mechanical rather than remembered: `scripts/pre-commit/check-domain-docs.sh` fails the build when this file's architecture tree stops naming exactly the Go packages under `internal/core`, at any depth (a family's packages may be written `family/{a, b}`), when a domain is described twice in the Purpose paragraph, or when one of the three ADR indexes (`docs/adr/CLAUDE.md`, `docs/CLAUDE.md`, this file's Reference list) drifts from the files in `docs/adr/` — both of which parallel union merges had actually produced here, along with two whole Purpose paragraphs coexisting while `events` was documented only in the stale one and `health` in neither.
12. **Every test excluded from normal discovery needs a named, executable, currently-green gate — or an explicit declaration that it must not run.** Exclusion mechanisms compound silently: `//go:build !race` hides a file from the race suite (race is on by default, see `.bazelrc`), `gazelle:excluded` + `manual` + `-test.run=^$` hides a target from `bazel test //...`, and a `.ktn-linter.yaml` entry hides it from the linter. Each exclusion is individually justified and documented; *together* they have already produced tests that nothing ever ran — including `pkg/v1/observe/logger`'s `TestV116BuildSendAllocatesOnePerEmit`, the regression guard for the allocation claim in this very file (a design contract since ADR 0165: `allocsMin: 1` on `logger.Build`, run as `TestPerfAllocsBuildSend`). Gates in force today: the race-off alloc lane (`tools/alloc-lane-targets.txt`, mechanically enforced by `scripts/pre-commit/check-alloc-lane-coverage.sh`, wired into `make lint` and CI) covers every `//go:build !race` test — and since ADR 0165 kit gen writes the lane's last section itself, naming every package whose design budgets name a fixture, so the race-off `gen_perf_test.go` it writes cannot run nowhere (`kit gen -check` fails on a hand edit inside it), and CI's `test-386` job compiles and runs those same files a second time on linux/386, where `-race` does not exist; `TestGenerateBenchMD` is opt-in by exact `-test.run` name under BOTH build systems; the Docker-backed `integration` suites declare their run procedure in `e2e/integration/CLAUDE.md` (they live in the auxiliary `e2e` module — ADR 0157), and every other tagged suite — the AWS writers' `localstack` ones and `encwrite`'s `integration` socket test — in its package `CLAUDE.md`. `//framework/internal/app:app_test` is `manual` under Bazel — the suite reads its own sources and positions relative to its module root — and `make test-framework` (`go test -race`, `GOWORK=off`, over `./framework/...` in the root module and in each of the four connector modules of the census, `connectors/ssh` included — a step of CI's `bazel` job and a gate of `scripts/ci-gates-check.sh`) is its lane (ADR 0147). Every test runs on every platform CI can execute, at full size — no lane passes `-short` (`.github/workflows/CLAUDE.md` §Test matrix is the table): Linux by Bazel (every package of `go.work` and `tools/`, race on), the alloc lane (race off), `make test-framework` and `test-386` (every package of every module, 32-bit); macOS and Windows by `e2e-cross.yml`'s `native` job (`go test ./...` in every module of the census); FreeBSD, OpenBSD, NetBSD, illumos and Solaris by its `bsd` and `solarish` jobs, whose guests run a `go test -c` binary of EVERY package with tests (`scripts/ci/test-binaries.sh`, `scripts/ci/run-test-binaries.sh`), not a list; Linux as root under systemd and OpenRC on four kernels (`linux-init`) runs the kernel-sensitive list. DragonFly, linux/arm and linux/arm64 compile and vet only (`cross-build`): no lane has a guest for them. A suite that cannot run on a platform skips there with its reason, after asserting the platform's contract (ADR 0095). When you add an exclusion, name its compensating lane in the same commit — and remember that a lane which exists but has been failing for weeks verifies nothing.

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

## Verification

| Command | Expected |
|---|---|
| `ktn-linter run --skip-phases=tests ./...` (`make lint-ktn-check`) | No issues found |
| `bazel build //...` | all targets pass |
| `bazel test --config=race //...` | every `*_test` target green (race on by default — see `.bazelrc`) |
| `bazel coverage --combined_report=lcov //...` | LCOV at `$(bazel info output_path)/_coverage/_coverage_report.dat` |
| `bazel query 'kind("go_library", deps(//internal/kernel/...)) except //internal/kernel/...'` | empty — kernel has zero outgoing go_library edges |
| `bash scripts/check-layer-deps.sh` | exit 0 — no layer reaches one above it: kernel → core → service → pkg/v1 → framework, `third-party/` above all but the framework (ADR 0068, ADR 0147; visibility alone does not bind `internal/` packages) |
| `make test-framework` | the framework's suites green under `go test -race`, `GOWORK=off` — `./framework/...` in the root module, then each connector module — the gate of the Bazel-`manual` `//framework/internal/app:app_test` (ADR 0147) |
| `make build` | `bazel mod tidy` + `bazel run //:gazelle` + `gofumpt -l -w` + `bazel build //...` |
| `make test` | every `*_test` target green incl. `//internal/kernel/errs:errs_test` (AST audit) |
| `make test-alloc` | race-off allocation gates green — every target in `tools/alloc-lane-targets.txt`, the only lane running `//go:build !race` tests |
| `bash scripts/pre-commit/check-alloc-lane-coverage.sh` | exit 0 — no `!race` test sits outside `tools/alloc-lane-targets.txt` (rule 12), every `allocwindow_test.go` — the allocation window a hand-written gate counts with, on its own call stack — is the same below its package clause, and no other hand-written test reads `runtime.MemStats`' `Mallocs` |
| `bash scripts/pre-commit/check-domain-docs.sh` | exit 0 — the architecture tree's one `core/` block still names exactly the Go packages under `internal/core`, at any depth, no domain is described twice, and the three ADR indexes name exactly the ADRs on disk, once each (rule 11) |
| `bash scripts/pre-commit/check-audit-coverage.sh` | exit 0 — no package declaring an `errs.Define`/`errs.Code` sits outside `//:audit_sources` (rule 3) |
| `bash scripts/pre-commit/check-core-symmetry.sh` | exit 0 — no production file under `internal/service` declares an error code, every service domain has a core package at the same path, and every code an `internal/core` package declares belongs to an engine at its path or, beneath a domain, to a range the core allocated (rule 3, ADR 0160) |
| `GOWORK=off GOARCH=386 CGO_ENABLED=0 go test ./...` (in every module `bash scripts/ci/go-modules.sh` prints) | green — the 32-bit RUNTIME bar, run by CI's `test-386` job over the whole census, `tools/` included and no module skipped (ADR 0137, ADR 0162). `cross-build` proves the SDK compiles on 386; this proves it behaves, which is where a `int(0xffffffff)` read as `-1` shows up |
| `make vuln-install && make vuln-check` | every module: no REACHABLE known vulnerability (`govulncheck` source mode, pinned in the `Makefile`; needs `vuln.go.dev`). Blocking in CI's `bazel` job and run daily by `vuln-scan.yml` (ADR 0136) |
| `go test ./pkg/v1/...` (in the workspace) | green; `pkg/v1/data/codec` completes in seconds — `TestGenerateBenchMD` self-skips unless named via `-run` |
| `make lint` | drift assertion (read-only): mod tidy + gazelle diff + gofumpt -l + `make guard` + `make doclinks` + `make api-check` + ktn-linter + alloc-lane coverage + audit coverage + domain-doc drift + core symmetry + platforms table + package docs + BENCH.md presence + error-code drift + README drift + layer firewall (`scripts/check-layer-deps.sh`) |
| `make guard` | `tools/sdkguard` over the SDK's own tree: the invariants (ADR 0033), then SDK002 over `internal/`, `pkg/`, `third-party/` and `framework/` — rule 2, no exemption (ADR 0161), then SDK006 over those and `tools/` and `e2e/`, test files too — no `net/http/pprof` import anywhere (ADR 0121, #1023); no network — `-version-check=off` |
| `make doclinks` | every same-package doc link in the repository names a symbol its package declares — a member of an aliased type is written `[Type].Member` (ADR 0138); part of `make lint-check`, so CI runs it |
| `make api-check` | exit 0 — `docs/api` regenerated from the code in memory, on every cell of `scripts/ci/platforms.sh`, equals what is committed byte for byte (a difference names each record added, changed or removed); on every cell the code's exported (id, kind, canonical signature) set equals the markers of the `gen_api*_test.go` pins kit writes from `design/`, and each symbol's doc and each package comment hash to the markers' doc digests (ADR 0167); and every generated file's header digests hold against its design file's bytes and its own body (ADR 0163). Part of `make lint-check`, so CI runs it, and named in `scripts/ci-gates-check.sh`'s GATES |
| `kit gen -check && kit check` (needs kit; no CI lane runs it — ADR 0163 §7) | exit 0 — the generated files are the design's, and the SDK holds to every rule of `design/sdk.yaml` with no finding: the surface (`api`, `ports`) and the structure (`layers`, `stdlib`, `mirrors`, `codes`, `facade`, `budgets`, `exceptions`), the latter advisory beside the SDK's own scripts; `kit gen -check` also re-measures the inline budget of each budgeted forwarder (ADR 0166) |
| `make regen` | exit 0, `regen: N files regenerated, zero diff` — every kit-generated file deleted and written back by `kit gen` from `design/`, then `make api`, with no difference and no file added that git does not track. Local only (needs the pinned kit); not in `make lint-check` nor any CI gate |
| `make from-zero` | exit 0, `from-zero: N generated files rebuilt from design/, 0 stub(s) on the way, zero diff` — in a temporary copy, every module compiles from the design and the bodies after `kit gen -stubs`, and after `kit gen` + `make api` the copy is the working tree. Local only (needs the pinned kit; runs bazel in the copy); not a CI gate |
| `make models-check` | exit 0, `kit design derive: the models are the code's — N package(s), …` — every model under `design/behaviour` is what the current code derives, none missing, none left over. Needs the pinned kit: local, and CI's blocking `models` job (the required `bazel` job needs it), which installs it with the secret `KIT_READ_TOKEN` and fails when the secret is absent — a fork's or Dependabot's pull request, which receives no secret, warns without checking |
| `kit check` (kit `v0.1.0-rc.15`, the pin of `design/sdk.yaml`) | green, the `inline` and `behaviour` rules among its rules — every model `design/behaviour` commits the current code's, and no function the base (`-base REF`, else HEAD) inlines that a generated declaration made stop inlining (ADR 0169); nothing measured on a clean tree |
| `make api` twice, then `git diff --exit-code docs/api` | no difference — the writer is deterministic: no timestamp, no absolute path, records sorted, the go command's environment pinned |
| `make docs-check` | exit 0 — the docs portal built for the working tree alone (`DOCS_RELEASES=local`, no network): its ⌘K index and the anchors of its API sections equal `docs/api` — every exported symbol of `pkg/v1`, and every method an alias reaches at its owner — counted by `docs/site/scripts/check-api-counts.mjs`, and every link and ⌘K entry resolves under the deploy base, fragments included (`check-links.mjs`). CI's `docs-site` job runs it, and `scripts/ci-gates-check.sh`'s GATES names it; the deploy runs the same checks over every release |
| `bash scripts/pre-commit/check-platforms.sh` | exit 0 — `scripts/ci/platforms.sh`, the table genindex judges doc links and writes docs/api on, names the cells of `bazel-ci.yml`'s cross-build matrix, in its order |

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
- ADR 0143 — a document keeps its versions in its own write (amended 2026-10-15: the trash) — `docs/adr/0143-a-document-keeps-its-versions-in-its-own-write.md`
- ADR 0144 — illumos and Solaris supervise, on the word of their own kernels — `docs/adr/0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md`
- ADR 0147 — the framework is a module of the SDK, above `pkg`, and a product imports nothing else — `docs/adr/0147-the-framework-is-a-module-of-the-sdk-above-pkg.md`
- ADR 0148 — a private socket is gated by its directory, and the kernel names the peer where it can — `docs/adr/0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md`
- ADR 0149 — a product reports numbers on a port, and its exporter only emits — `docs/adr/0149-a-product-reports-numbers-on-a-port-and-an-exporter-only-emits.md`
- ADR 0150 — a release names its tag, its keys rotate, and a replacement answers before it stands — `docs/adr/0150-a-release-names-its-tag-keys-rotate-and-a-replacement-answers-before-it-stands.md`
- ADR 0151 — a message published in a transaction exists if and only if it commits — `docs/adr/0151-a-message-published-in-a-transaction-exists-if-and-only-if-it-commits.md`
- ADR 0152 — consensus domain (`consensus`), accepted on 2026-10-13: a write is committed when a majority holds it on disk — a Raft written in the SDK (`internal/core/app/consensus` `0.2.57.*`, `internal/service/app/consensus` `0.3.92.*`, `pkg/v1/app/consensus`, constructor `NewNode`, no registry, stdlib only); PreVote and CheckQuorum always on; pipelined replication batched with no linger timer, acknowledged after `fsync`, a failed `fsync` stops the node; snapshots and compaction in v1; single-server membership changes through learners, node IDs never reused, explicit `Bootstrap`; leadership transfer; ReadIndex by default, a lease only with a stated `MaxClockDrift`; `(term, index)` on every entry, the index a fence; one group, `GroupID` on the wire; a deterministic simulator with a disk model and an in-tree linearizability checker with a negative control; alternatives rejected by the owner; confronted with the owner's prior raft-sql-poc in two lists, what it does well and is taken, what it does badly and is not; its measured answers amend the hard state, transport admission, elections on restart, transfer, snapshot compression, the outcome of a timed-out proposal and the mutation controls; and with the owner's Go system IWFS, an in-memory Raft-like log whose quorum counts the members a health check sees, whose term and vote are not persisted and whose mesh is plaintext, from which it takes the vote refusal under a live leader, a wait on index and term (ENTRY_SUPERSEDED), refusal before append (OVERLOADED), the warm dial to the leader, a priority lane for control frames and four defect scenarios; reworked after round 1 of its challenge: a pure Apply over a replicated ref table with Git as an asynchronous projection, a durable applied position, a storage actor owning durability, narrow tail repair, three connections per peer, certificate identity checked both ways, the lease kept with every correction, operations and versioning in v1; after round 2 the SDK ships them as Go functions only, with no package main, no admin handler and no CLI, Forgejo administering the cluster natively, from any node, and running the replicator in process; first review run stopped at three rounds; review run 2 round 1 applied as R4 (divergence handling fully automatic with guardrails, durable state-machine ports kept); review run 2 round 2 applied as R5: split from ADR 0173 (divergence), snapshots back to the core model, sync watermark, restart gc barrier, git transaction tracking, symbolic refs in the table, mandatory backup reconciliation; review run 2 complete after round 3 applied as R6 (two retained snapshots, implicit sync watermark, ADR 0173 activated all or nothing by the cluster version), review run 3 round 1 applied as R7: aligned with the SDK's design file (`design/app/consensus.yaml`), the `app` family and one module, numbered log batches with logical truncation, durable `Recover` fences, Vault certificates per slot, the pre-handshake admission sibling, SQL derived by a log consumer, the Nomad deployment of Q13 with slots and `JOIN`, and the infrastructure prerequisites listed in D20; review run 3 round 2 applied as R8: one log-recovery procedure, keys pinned by SPKI, slots pinned on Nomad node ids, a published JOIN surface, a Recover witness on the NAS that fails closed, the SQL event lifecycle; review run 3 round 3 (final) applied as R9: contiguous batches, signed witness receipts from both NAS blocking Forgejo only, SPKI required at founding and recovery; state rewritten after the final round (R9), not re-reviewed — `docs/adr/0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md`
- ADR 0153 — the repository carries no devcontainer and no git hooks, and its guards run in CI — `docs/adr/0153-the-repository-carries-no-devcontainer-and-no-git-hooks.md`
- ADR 0154 — the SDK's principles are one charter, and an incident's rule lives with the code it hit — `docs/adr/0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md`
- ADR 0155 — every layer groups its packages by family, at the depth the families need, and an import path may move while v0 — `docs/adr/0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md`
- ADR 0156 — the public module links the standard library and nothing else — `docs/adr/0156-the-public-module-links-the-standard-library-and-nothing-else.md`
- ADR 0157 — one module per vendor, released with the SDK — `docs/adr/0157-one-module-per-vendor-released-with-the-sdk.md`
- ADR 0158 — distribution mechanisms are the framework's, not the SDK's — `docs/adr/0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md`
- ADR 0159 — the kernel holds what the domains were rewriting, and is published by nature — `docs/adr/0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md`
- ADR 0160 — every service has a core, and a code keeps its value when its declaration moves — `docs/adr/0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md`
- ADR 0161 — an untyped error fails the build — `docs/adr/0161-an-untyped-error-fails-the-build.md`
- ADR 0162 — the SDK is one module, and a release is one tag — `docs/adr/0162-the-sdk-is-one-module-and-a-release-is-one-tag.md`
- ADR 0163 — the SDK is designed by its diagram, and kit writes only data and test pins — `docs/adr/0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md`
- ADR 0164 — an error code is the design's, and kit writes it — `docs/adr/0164-an-error-code-is-the-designs-and-kit-writes-it.md`
- ADR 0165 — a performance contract is the design's: kit measures what the compiler decides, and a test holds the rest — `docs/adr/0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md`
- ADR 0166 — a facade is the design's, and a forwarder's form is measured — `docs/adr/0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md`
- ADR 0167 — a doc comment is the design's, and a README is written from docs/api — `docs/adr/0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md`
- ADR 0168 — a declaration is the design's, and a body is the code's — `docs/adr/0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md`
- ADR 0169 — the SDK is rebuilt from its design in a copy, and a generation is held to the whole inline set — `docs/adr/0169-the-sdk-is-rebuilt-from-its-design-in-a-copy-and-a-generation-is-held-to-the-whole-inline-set.md`
- ADR 0171 — a module per pkg family, one shared internal module, and a published version is never reused — `docs/adr/0171-a-module-per-family-and-a-version-is-never-reused.md`
- ADR 0172 — a product initialises what it imports, and the stores stay in the core — `docs/adr/0172-a-product-initialises-what-it-imports-and-the-stores-stay-in-the-core.md`
- ADR 0173 — divergence of replicated state, proposed, split from ADR 0152 by the owner's decision Q12 and depending on it: a canonical incremental state hash compared at fixed indexes, self-proposed signed digests decided in Apply, automatic repair with guardrails (a twice-agreeing rebuild that matches a validated digest, no rebuild storm, a defined self-withdrawal), anchors on top of 0152's checksummed snapshots, a behavioural Apply fingerprint and replay gate, a stdlib determinism checker; codes keep their serials in 0152's 0.2.57 range; the open objections of 0152's challenge are the input of its own challenge, still pending; ADR 0152's R7 contract recorded (paths, code ownership in the same core package, the snapshot pin, `Message` v2 fields in the encoded frame) — `docs/adr/0173-divergence-is-prevented-by-construction-and-repaired-automatically.md`
- ADR 0174 — TLS identity and pre-handshake admission, accepted on 2026-10-13 with ADR 0152, reserved by ADR 0152 for its two siblings outside the consensus domain: a `net/tlsid` identity checked on every handshake (fail-closed `VerifyConnection`, the verified peer and its SPKI handed to the caller, a reloadable source refusing a new key or SAN, a CA rollover trusting the union), and a `net/server` `Admission` that closes a connection over its IPv4 address's or IPv6 /64's budget before any TLS byte is read, with a reserved budget for declared member addresses — `docs/adr/0174-a-tls-identity-is-checked-on-every-handshake-and-reloaded-in-place.md`
- ADR 0175 — a product's tests reach the disk with one option, and the last of InMemory and DataDir wins: `framework/kit/kittest`, ported from kitsunium/platform `d1e5961` (platform#35 item 6, sdk#274): `kittest.NewDisk(t)` is a data directory of the test's own with the secrets kit keeps beside it, and an `AppOption` that runs the suite's in-memory helper on disk, where kit seals and transactions write files; `Disk.Holding` names the files that keep a word in clear; it skips where the SDK's disk file system refuses the platform (Windows). `kit.DataDir` given after `kit.InMemory()` now wins — the last of the two says where the data is; an `InMemory` after a `DataDir` still keeps everything in memory — `docs/adr/0175-a-products-tests-reach-the-disk-with-one-option.md`
- ADR 0176 — An error sentinel is static data, and its options are the design's fields, proposed on 2026-10-14: the 679 `errs.Define` calls of the 93 generated `gen_codes.go` run at init because `Define` validates, can panic and applies closures; a sentinel gains `exit:` (0..255 or a sysexits name) and `http:` (100..599) in the design, `options:` stays for the dynamic only, and a sentinel without options is written with a new `errs.Static`, a sibling of `Define` (constants only, one composite literal, initialised at link time) whose validation moves to `kit gen` and whose registry audits learn it; five PRs, the first gated on proof that the toolchain initialises it statically; the gain is capped at 0.1 to 0.2 ms of init (ADR 0172) — `docs/adr/0176-an-error-sentinel-is-static-data-and-its-options-are-the-designs-fields.md`
- ADR 0177 — the Linux init runtime bar runs on public runners, and the lab is gone — `docs/adr/0177-the-linux-init-runtime-bar-runs-on-public-runners.md`
- ADR 0178 — kit's effects join the transaction on the database that keeps their queue, accepted on 2026-10-14 for #274 by the owner's decision on each of its seven choices; the subject in a list (C5) implemented; three more points decided on 2026-10-15 (no index of the oldest `data-key` version, the age limit on versions still deferred, `kit.Only`, `kit.As` deferred) — `docs/adr/0178-kits-effects-join-the-transaction-on-the-database-that-keeps-their-queue.md`
- ADR 0179 — a store is a non-generic engine under a typed facade, and a subsystem is imported, accepted on 2026-10-16 for #273 and #276 by the owner, who retained the recommendation of each of its eight questions: one registration package per subsystem, a non-generic store engine under `Store[T]`, `kit gen` writing the imports, a typed refusal at the start; implemented on 2026-10-17 — step 10 the break: the facade imports no subsystem, a product imports those it declares, and `kit.Migration` is kit's own — `docs/adr/0179-a-store-is-a-non-generic-engine-under-a-typed-facade-and-a-subsystem-is-imported.md`
- ADR 0180 — the runtime framework is not named after the tool, and a product carries no trace of it, accepted on 2026-10-17 by the owner, who named the framework `framework/app` and retained every other recommendation: kit is a development tool, the framework is renamed `framework/app`, every runtime name follows, the Studio is injected by `kit dev` through `go build -overlay`, and a guard refuses the token in a reference product's tree, binary and execution; amends ADR 0147 §1 and ADR 0179; step 1 done (the Studio's routes and the tool's variables out of the core, into `studiokit` behind `plug.Studio`); step 3 done (`framework/kit` is `framework/app`, `framework/internal/kit` is `framework/internal/app` with its engines named `*engine`, `model.Version` 6, the framework's texts name the framework); step 4 done (the runtime names: the operator's variables under the product's prefix, `/_app/health/*`, `app:"…"`, the graph's `"app"`, `app-telemetry/v1`; an old variable or tag key refused at the start, `CodeRenamed`); step 5a done (the names at rest moved: the `kit_*` tables are `app_*`, `.kit/data` is `.app/data`, `kit.privacy` is `app.privacy`; `CodeRenameTarget`, `CodeRenameFailed`; the secret prefix and the sealing's labels left to step 5b's re-seal); step 5b done (the framework's secrets are `app-…`, `kit-index-key` copied once and `kit-smtp-url` once then again only when the former holds the newer value, every data key re-wrapped under `app-data-key` through the SDK's `SubjectKeysConfig.Former`, a box under data-key itself sealed under `app/v1 sealed at rest`, the former names read for the window, `privacy rewrap [-back]`; the references' labels left to step 5c); steps 5c-1 and 5c-2 done (the privacy journal closed and continued, the references at rest sealed again under the `app/v1` labels); step 6 done (`TestAProductCarriesNoTraceOfTheTool` on `framework/app/testdata/traceprobe`: no trace in the tree, the release binary or the execution, but the former names its allow-list admits, each until the step that removes it) — `docs/adr/0180-the-runtime-framework-is-not-named-after-the-tool-and-a-product-carries-no-trace-of-it.md`
- ADR 0181 — a generated file's name opens with `gen_` (the owner's decision on kitsunium/studio#74, kit's ADR 0014): `gen_api*_test.go`, `gen_ports.go`, `gen_codes.go`, `gen_ranges_test.go`, `gen_perf_test.go`, `gen_facade.go`, `gen_decls.go`, `gen_stubs.go`; the generated files linted, the rules unfit for them leaving `**/gen_*.go` out — `docs/adr/0181-a-generated-files-name-opens-with-gen.md`
- ADR 0182 — a runtime directory ends on the caller's directory, never on a shared tree, accepted on 2026-10-20 for kodflow/ktn-linter#785: `ipc.RuntimeDirOr(app, fallback)` ends `RuntimeDir`'s chain on a directory the caller keeps private instead of `<tmp>/<app>-<uid>`; `RuntimeDir` unchanged — `docs/adr/0182-a-runtime-directory-ends-on-the-callers-directory-never-on-a-shared-tree.md`
- ADR 0183 — a message may wait for its instant, and recurrence stays the scheduler's, accepted on 2026-10-21 for kitsunium/sdk#481, decided autonomously and revisable while v0 (ADR 0040): resolves ADR 0054's deferred `PublishAt` — one message held until one instant, durably, recurrence left to ADR 0041's scheduler — and adds on every broker `DelayedPublisher`, `DelayedNacker` with the handler's `RetryAfter` (`RETRY_REQUESTED` `0.2.23.10`) and `OncePublisher` (a duplicate is a boolean) — `docs/adr/0183-a-message-may-wait-for-its-instant-and-recurrence-stays-the-schedulers.md`
- ADR 0184 — a wait is bounded, a restart gives up, and a client throttles itself, accepted on 2026-10-21 for kitsunium/sdk#482, decided autonomously and revisable while v0 (ADR 0040): resolves ADR 0026's deferred wait mode with `NewBulkheadWait` and `RateLimiterConfig.MaxWait`, adds full and decorrelated jitter (`JitterMode`), `BreakerConfig.HalfOpenTrials`, the supervisor's `GiveUpAfter` with `Supervisor.Check` and `SUPERVISOR_GAVE_UP` (`0.3.49.9`), the scheduler's `StartingDeadline` and `OverlapReplace`, and the SRE book's adaptive throttle `NewAdaptiveThrottle` (`THROTTLED`, `0.2.8.9`); adaptive concurrency deferred — `docs/adr/0184-a-wait-is-bounded-a-restart-gives-up-and-a-client-throttles-itself.md`
- ADR 0185 — an export waits on disk, and the forwarder says what it lost, accepted on 2026-10-21 for kitsunium/sdk#493, decided autonomously and revisable while v0 (ADR 0040): a generic store and forward in `app/forward` (codes `0.3.93.*`), beside the mail spool because the module graph forbids `observe` (it would require `app`) and `data`; one queue message per batch, the OTLP/JSON request the signal already encodes, posted unchanged through a new sibling port per signal, `trace.DocumentExporter` / `metrics.DocumentExporter`; one queue per signal, one consumer by default (order kept, and the load on a down collector one request); bounded in batches AND bytes (`DefaultMaxBatches` 10 000, `DefaultMaxBytes` 256 MiB, negative refused); when full `DropOldest` (the zero), `DropNewest` or `Block` up to `BlockTimeout`; each batch retried in the handler by `resilience.NewRetry` on the injected clock, `Retry-After` honoured in full up to the batch's `MaxAge` (24 h), otherwise 5 s growing to 30 s; a permanent refusal dead-lettered once (`DoNotRetry`), an expired batch dropped and counted; `Stats` counts pending, sent, retries, dropped, expired, dead-lettered and the last `Retry-After`; the stop is `Consume`'s drain; the framework's OTLP subsystem and the Studio deferred; amended on 2026-10-23 for kitsunium/sdk#580: the count follows the queue, by identifier, the queue listed again within `RecountInterval` (one minute) of a read of `Stats` or a settle, at once, once per interval at most, on the delivery of a batch the forwarder did not count — `docs/adr/0185-an-export-waits-on-disk-and-the-forwarder-says-what-it-lost.md`
- ADR 0186 — a plaintext group may run on an event loop, and its HTTP is a profile that refuses, accepted on 2026-10-23, revisable while v0 (ADR 0040): amends ADR 0029 D1–D4 with an opt-in, plaintext-only loop engine of `net/server` entered by `StreamGroup.HandleLoop` (io_uring and epoll on Linux, portable elsewhere; kqueue and IOCP deferred) and a restricted HTTP/1.1 profile, `net/http1`, that refuses chunked, trailers and `Expect: 100-continue`; orders the public surface, `HandleLoop` last; amends ADR 0159 §4 with `swar` (not published) and `jsonenc` — `docs/adr/0186-a-plaintext-group-may-run-on-an-event-loop-and-its-http-is-a-profile-that-refuses.md`
- ADR 0187 — the handler type chooses the engine, accepted on 2026-10-25, revisable while v0 (ADR 0040): amends ADR 0186 D1–D3 — a `LoopHandler` runs on the loop and every other handler on the goroutine engine; every `GroupOption` records its `OptionKind` and a refusal reads that set, so a zero-valued option on a loop group is refused; `Plaintext`, `Name` and `Engine`, honoured by every engine, contradictions refused with `EngineUnsupported`, `PlaintextWithTLS` or `GroupNameConflict`; carries the simple API (`srv.HTTP/TCP/UDP`), `Plaintext` required on HTTP without TLS (staged), the environment that only restricts and gates at the merge — `docs/adr/0187-the-handler-type-chooses-the-engine.md`
- ADR 0188 — the operator's switches only restrict, and State says what the server runs without, accepted on 2026-10-25, revisable while v0 (ADR 0040): amends ADR 0187 §D8 — the capability registry in `design/net.yaml`; `KITSUNIUM_NET_ENGINE` / `KITSUNIUM_NET_DISABLE` read once by `net/server.New` before any probe, the effective backend min(code, environment), a ruled-out backend never probed, an unknown token capping at portable; `StateValue` gains `Override`, `Faults` (aggregated by `Degraded`), `Fallbacks` and `Platform`; `Server.RegisterHealth` — readiness from `State`, liveness failing 30 s after a loop group's every ring drained, when `Serve` returns `LoopGroupStalled`; the `net:default-change` label gated on the pull request and in the merge queue — `docs/adr/0188-the-operators-switches-only-restrict-and-state-says-what-the-server-runs-without.md`
- ADR 0189 — a kernel capability is probed, reported, and switched off by name, accepted on 2026-10-25, revisable while v0 (ADR 0040): amends ADR 0188 §D1 and ADR 0186 §D5 — the kernel capabilities join the registry in three classes (A-kernel on once probed, A-code opt-in through `LoopValue.Capabilities`, B declared and refused until a BENCH.md threshold), each switched off by name in `KITSUNIUM_NET_DISABLE` and never probed then, an unknown token leaving none; the io_uring probe descends flag by flag on throwaway rings under the thread's lock (`EEXIST` a probe defect), `DEFER_TASKRUN` on the primary ring only, `NO_SQARRAY`, `RING_FDS` unregistered before close, `SQ_REWIND` with its partial-submit self-test; `LoopStateValue.Capabilities` with server's `tcp-nodelay` row, an opted-in capability the kernel lacks a `capability` fault; `LoopCapabilityRefused` (`0.2.11.60`); the lanes' `requiredCaps` — `docs/adr/0189-a-kernel-capability-is-probed-reported-and-switched-off-by-name.md`
- ADR 0190 — a merge gate judges the ratio of two engines measured side by side, accepted on 2026-10-26, revisable while v0 (ADR 0040): amends ADR 0187 §D10 — A and B in one child, windows A, B, B, A, …, `tools/netgate` holding each threshold on the interval of the rounds' ratios, a calibration row a load — `docs/adr/0190-a-merge-gate-judges-the-ratio-of-two-engines-measured-side-by-side.md`
- ADR 0191 — a loop middleware forwards every callback, and the http1 chain judges one canonical target, accepted on 2026-10-25, revisable while v0 (ADR 0040): amends ADR 0186 §D3, §D6 and ADR 0187 §Deferred — `Loops` lanes on every backend, `LoopStarter.OnStart`, `HTTP1RequestValue.Lane`; `StreamGroup.UseLoop`, `LoopLayer`, a probe chain at Start and `LoopMiddlewareInvalid` (`0.2.11.61`); the canonical target and the http1 links in its second change — `docs/adr/0191-a-loop-middleware-forwards-every-callback-and-the-http1-chain-judges-one-canonical-target.md`
- ADR 0192 — a datagram socket reads coalesced, and its replies may leave per batch, accepted on 2026-10-25 and 2026-10-26, revisable while v0 (ADR 0040): every `recvmmsg` restores its capacities and reads `msg_flags` back, a cut message dropped and counted; UDP GRO wherever the cmsg reader is, one `Packet` a datagram; `Shutdown` drains a coalescing socket with GRO on, then switches it off, after the stream listeners close and within 250 ms; `Reply` synchronous unless `BatchReplies()`, which queues a batch's replies — `sendmmsg` and UDP GSO on Linux, one `WriteTo` each elsewhere —, a stream group refusing it; L3's gated CPU a datagram is the receiver's and the sender's rusage plus /proc/stat's irq and softirq time, metered alike in the paired windows of ADR 0190, 400 000 datagrams an engine a round (2026-10-26), datagrams a receive reported, not gated
- ADR 0193 — a Windows listener holds its address exclusively, closes gracefully and rebinds, bounded, accepted on 2026-10-27, revisable while v0 (ADR 0040): `SO_EXCLUSIVEADDRUSE` on unicast TCP/UDP, read back on an adopted socket, `shutdown(SD_SEND)` + read to zero, a bounded rebind; `ListenerStateValue.Exclusive`, `BindWait` — `docs/adr/0193-a-windows-listener-holds-its-address-exclusively-closes-gracefully-and-rebinds-bounded.md`
- ADR 0194 — a loop lane offloads to its own worker, and an http1 answer may be pending, accepted on 2026-10-27, revisable while v0 (ADR 0040), Q1–Q3 the lead's while the owner was away: `LoopOutbound`, `LoopWorker`, `LoopOffloader`, `LoopJobValue`, `LoopOutboundValue`, `LoopOutboundFull` (`0.2.11.63`), `HTTP1Worker`, `HTTP1ResponseValue.Pending`/`Job`, `LoopValue.Outbound`, `LoopStateValue.OutboundRefused` — `docs/adr/0194-a-loop-lane-offloads-to-its-own-worker-and-an-http1-answer-may-be-pending.md`
- ADR 0195 — an HTTP group's protocols are computed once, and its ALPN follows them, accepted on 2026-10-27, revisable while v0 (ADR 0040): `Protocols`, `OptionProtocols` (21; 20 is ADR 0192's `OptionBatchReplies`), `ProtocolsRefused` (`0.2.11.62`), the listener's ALPN and `http.Server.Protocols` from one list, HTTP/2's bounds — `docs/adr/0195-an-http-groups-protocols-are-computed-once-and-its-alpn-follows-them.md`
- ADR 0196 — a drain stops the routing first, takes the listen queue, then closes, and LoopsAuto reads GOMAXPROCS once, accepted on 2026-10-27, revisable while v0 (ADR 0040): amends ADR 0186 §D1, §D5 and ADR 0187 §D9 — `corenet.LoopsAuto`, `LoopStateValue.LoopsSource`, `server.WithDrainGrace`; the loop drain takes its listen queue and its last taker closes the listener; P6's gate active — `docs/adr/0196-a-drain-stops-the-routing-first-takes-the-listen-queue-then-closes-and-loopsauto-reads-gomaxprocs-once.md`
- ADR 0197 — the simple API places a handler by its type, and an HTTP group says Plaintext, accepted on 2026-10-27, revisable while v0 (ADR 0040): `Server.HTTP`/`TCP`/`UDP`, `HandlerAmbiguous`, `HandlerUnsupported`, `PlaintextRequired`, `PacketOptionUnsupported` (`0.2.11.64` – `0.2.11.67`); breaking: an HTTP group without TLS needs `Plaintext()`, a datagram group refuses options it cannot honour — `docs/adr/0197-the-simple-api-places-a-handler-by-its-type-and-an-http-group-says-plaintext.md`
- ADR 0198 — the http1 codec takes TLS on the portable backend, accepted on 2026-10-27, revisable while v0 (ADR 0040): crypto/tls handshake on its own goroutine bounded by `HandshakeTimeout`, ALPN `http/1.1` only (`Protocols("http/1.1")` honoured, any list naming `h2` `ProtocolsRefused`), TLS 1.2 floor on every server listener, the codec on the portable lanes; `LoopTLSUnsupported` for a raw `LoopHandler`, a zero identity, a native backend, `UseLoop`; `http1.LoopHandler.Unwrap` — `docs/adr/0198-the-http1-codec-takes-tls-on-the-portable-backend.md`
- ADR 0199 — a loaded io_uring loop groups its wait, and a native loop yields to a spare P, accepted on 2026-10-28, revisable while v0 (ADR 0040): amends ADR 0196 §D1 — `LoopValue.BatchWait` (250 µs) and `BatchWaitMin` (32) after a batch of four completions, `runtime.Gosched` every 5 ms while GOMAXPROCS exceeds the loops, `LoopsAuto` GOMAXPROCS less one; `uring-min-timeout` stays class B — `docs/adr/0199-a-loaded-io-uring-loop-groups-its-wait-and-a-native-loop-yields-to-a-spare-p.md`
- ADR 0200 — an io_uring close is a shutdown alone, and the accept watchdog judges a listener, accepted on 2026-10-31, revisable while v0 (ADR 0040): amends ADR 0196 §D3's cost — the watchdog judges no stall while another ring of the group accepts on the listener; a close is `IORING_OP_SHUTDOWN` alone (the linked `FD|ALL` cancel walked every armed request: a drain of 60,000 took 249 s), a user_data cancel backstops it after 1 s; an overflowed CQ is flushed by an enter with GETEVENTS
- ADR 0201 — an io_uring ring's buffers follow its bursts, and a slot table grows by steps, accepted on 2026-10-31, revisable while v0 (ADR 0040): 8,192 provided buffers of 2 KiB mapped and handed to the kernel 512 at a time as recvs need them (1 MiB idle instead of 16 MiB per ring); the slot table doubles to 2,048 slots, then grows 2,048 at a time; `uring-pbuf-inc` stays class B
- ADR 0202 — a loop batch pays its fixed cost once, and a grouped wait starts on the request rate, accepted on 2026-10-31, revisable while v0 (ADR 0040): amends ADR 0199 §D1 — a batch reads the clock once at each end, the accept watchdog looks once a tick, a wait with a ready CQ makes no wait; the grouped wait also starts when two arrivals at least are expected within `BatchWait` (moving average of their spacing), and an empty grouped window is waited through with no batch; `uring-min-timeout` stays class B, measured — `docs/adr/0202-a-loop-batch-pays-its-fixed-cost-once-and-a-grouped-wait-starts-on-the-request-rate.md`
- ADR 0203 — an io_uring loop's rings fill in order, accepted on 2026-11-01, revisable while v0 (ADR 0040): a ring past the first accepts only once those before it are within an eighth of their table (hysteresis at twice that), so a load one ring holds costs one ring whatever `MaxConns` derived; amends ADR 0201 §D1 — a ring's buffers up to its table (65,536 × 2 KiB, groups of 8,192 registered as needed), grown by the recvs starved, re-armed only on buffers the kernel holds, 128 MiB a loop — `docs/adr/0203-an-io-uring-loops-rings-fill-in-order-and-a-rings-buffers-follow-its-table.md`
- ADR 0204 — an io_uring ring's buffers are consumed incrementally where the kernel allows, accepted on 2026-11-01, revisable while v0 (ADR 0040): amends ADR 0189 and 0201 — `IOU_PBUF_RING_INC` on every buffer group, a buffer returned only after its completion without `BUF_MORE`, `EINVAL` falls back; `uring-pbuf-inc` class A-kernel, `KITSUNIUM_NET_DISABLE=uring-pbuf-inc` restores a buffer a recv — `docs/adr/0204-an-io-uring-rings-buffers-are-consumed-incrementally-where-the-kernel-allows.md`
- ADR 0205 — an io_uring recv may fill several buffers, and only on request, accepted on 2026-11-02, revisable while v0 (ADR 0040): amends ADR 0189 — `IORING_RECVSEND_BUNDLE` behind the class A-code `uring-recv-bundle` (`server.CapabilityURingRecvBundle`), a probe bundle recv must fill two buffers (none with incremental consumption before 6.15), a completion walked buffer by buffer in ring order; measured no gain, 25 % slower on 8 KiB, so opt-in — `docs/adr/0205-an-io-uring-recv-may-fill-several-buffers-and-only-on-request.md`
- ADR 0206 — an io_uring batch may be bounded in time, accepted on 2026-11-02, revisable while v0 (ADR 0040): `LoopValue.BatchBudget` stops a batch's reaping once spent (reaps of 16), the next batch starts at the cut ring without waiting, a batch past half its budget submits its answers at once, `BatchesCut` in State; zero changes nothing — `docs/adr/0206-an-io-uring-batch-may-be-bounded-in-time.md`
- ADR 0207 — concurrent callers share one flush, and each gets its verdict, accepted on 2026-11-02: the kernel primitive `concur/commit` (`Group[T]`, `New(flush, Config{MaxItems, MaxWait, Clock})`, `Do`, `Close`, `COMMIT_CLOSED` `0.1.4.1`) — flushes one at a time in batch order, on a goroutine of their own, a departed caller's item still flushed, a panic re-raised in every participant; its first adopter is vfs `WriteAtomic`'s directory flush, shared per directory with no API change; the others (vfs `WriteAtomicAll`, queue `PublishAll`, docstore, mail spool, logger flush) are siblings of their domains; amended 2026-11-12 (#813): an idle `Do` whose context can never end flushes on its caller's goroutine — `docs/adr/0207-concurrent-callers-share-one-flush-and-each-gets-its-verdict.md`
- ADR 0208 — an instrument gets a unit, and it belongs to the NAME, accepted on 2026-11-03 (kitsunium/sdk#1021), resolving ADR 0067's deferral: a `UnitDescriber` sibling (`Unit(name, unit string)`) found by type assertion, not in `FullMeter`; `Unit` beside `Description` on the three metric envelopes; an empty, over-63-byte or non-printable-ASCII unit panics `INVALID_UNIT` `0.2.9.9`, a second differing one `UNIT_CONFLICT` `0.2.9.10`; OTLP/JSON `Metric.unit` omitted when empty, the text exporter's ` unit="…"`, and no Prometheus suffix — `docs/adr/0208-an-instrument-gets-a-unit-and-it-belongs-to-the-name.md`
- ADR 0209 — a document is replaced on the revision it was read at, accepted on 2026-11-02 (kitsunium/sdk#1055): `core/data/docstore`'s `Revisioned[T]` / `RevisionedContext[T]` (`Revision`, `ReplaceIf`), the opaque `RevisionValue` and `REVISION_MISMATCH` `0.3.80.23`; amended the same day for several writes as one, `Batcher[T]` / `BatcherContext[T]` (#1057), and on 2026-11-03 for the documents a page at a time and an ordered index, `Pager[T]` / `Ranged[T]` and `Ordered` (#836) — `docs/adr/0209-a-document-is-replaced-on-the-revision-it-was-read-at.md`
- ADR 0210 — a published JSON Schema is validated with the standard library, accepted on 2026-11-03 (kitsunium/sdk#882): `codec/jsonschema` (`0.3.103.*`, facade `pkg/v1/data/codec/jsonschema`) — `NewValidator(schema, Config)` compiles 2020-12's core, applicator and validation vocabularies, `$ref` within the document and to `Config.Resources`, never fetched, numbers exact, `format` asserted only for `AssertFormats` (date-time, uuid, email); another draft, `$dynamicRef`, `$vocabulary`, the unevaluated and content vocabularies refused by name; `Validate(doc, maxBytes)` reads through strictjson and returns a `validation.ReportValue` in its path grammar, never quoting the document; the official JSON-Schema-Test-Suite vendored and passed but the named groups
- ADR 0211 — the data ports' vendor adapters, accepted on 2026-11-02: a Valkey cache tier and stream broker in `third-party/db/redis` (named by its vendor, #984), a JetStream broker in `third-party/nats`, an S3 `FullFS` and an SQS broker in `third-party/aws`; Valkey leases on the broker's clock, JetStream and SQS leases on the server's, refused at their deadline before anything is sent; the broker dead-letters with the cause — `docs/adr/0211-the-data-ports-vendor-adapters-valkey-jetstream-s3-and-sqs.md`
- ADR 0213 — a compression stream is read under the caller's ceiling, accepted on 2026-11-03 (kitsunium/sdk#892): `core/data/transform`'s sibling port `StreamingCompressor` (`NewReader(r, max)`, `NewWriter(w)`) for gzip, flate, zlib, zstd and s2, at most `max` bytes then `DecompressedTooLarge`, the facade's `NewReader` / `NewWriter` and `STREAMING_UNSUPPORTED` `0.2.5.7`; closes ADR 0066's streaming deferral — `docs/adr/0213-a-compression-stream-is-read-under-the-callers-ceiling.md`
- ADR 0214 — a service unit is one actor over a pure reducer, accepted on 2026-11-03 (kitsunium/sdk#732, S0a of supervizio/agent#326): the life cycle of ONE service moves into the SDK as `proc/service` (amends ADR 0016's Context), not a lifecycle `Component`, not `lifecycle.Supervisor`, not `statemachine`, and importing nothing of `app`; one goroutine per unit, a pure `reduce(model, event) → (model', effects)` and an I/O shell; observers on a bounded queue the unit never waits on, the newest event dropped and counted, `State()` a level with a generation; every wait on the kernel's `clock.Timed`; the ports `Spawner`, `Attacher`, `CgroupKill`, `NotifySource`, `Clock`; a stop escalates on the unit's own context; `Fail` is `WATCHDOG=trigger` from outside; Restart= Table 1 as the oracle, replayed; the coverage of every directive of `systemd.service(5)`, `systemd.kill(5)`, the launch part of `systemd.exec(5)` and the start limit of `systemd.unit(5)`; range `0.3.105.*`; amended 2026-11-08 (kitsunium/sdk#735, #736, #740 — S0c, S0d, S0f): `Type=forking` adopting its daemon from `PIDFile=` or guessing it among the cgroup's members (`CgroupMembers`), a descriptor store the shell owns handed back to the next main process, `SpawnInstance` — a unit per accepted connection, the connection at fd 3; amended 2026-11-08 again (kitsunium/sdk#734, S0g): the cgroup modes on a `CgroupUnit`, a sibling of `CgroupKill` the Linux group of `proc/cgroup` is — `KillMode`'s zero `KillModeDefault`, control-group with one and process without, the group's kill through `Signal` and never `cgroup.kill` (after which Linux 6.12 kills every child cloned into the group), `ExitType=cgroup`, `Delegate=` (the group handed, what the service made beneath it removed), `OOMPolicy=` on `memory.events`, the group read again whenever the reducer decides; `Members` over the subtree and `cgroup.Membership`, a group's directory as `/proc/<pid>/cgroup` names it; amended 2026-11-08 a third time (kitsunium/sdk#731, S6, ADR 0215): the BPF rows restated — `proc/bpf` attaches device, IP and bind policies to a group, a primitive the engine does not call yet, the agent attaching them to the unit's group; `RestrictFileSystems=`, `PrivateBPF=` and `BPFDelegate…=` no, none of them a cgroup program; amended 2026-11-08 a fourth time (kitsunium/sdk#728, S2, ADR 0217): the sandbox rows restated — `Spec.Sandbox` reaches every command through `Config.Spec`, no `Config` field takes a directive yet, `+` keeps the sandbox, the store's key ignores it; joining a namespace and the argument filters (`RestrictNamespaces=`, `RestrictAddressFamilies=`, `MemoryDenyWriteExecute=`…) no; amended 2026-11-08 a fifth time (kitsunium/sdk#737, S0e): `Type=dbus` up once its main process runs and its `BusName=` is owned, stopped when the bus announces it lost, systemd 262's two-second `running-revalidating` grace otherwise, through the `BusWatcher` port — the spike's option (a), `proc/dbus`, a message bus client on the standard library (AUTH EXTERNAL, `AddMatch`/`GetNameOwner`, every call bounded at 25 s, range `0.3.108.*`); amended 2026-11-08 a sixth time (kitsunium/sdk#728, #733): the sandbox wired into the engine — `ProtectSystem=` (`strict` no), `PrivateTmp=disconnected` (`yes` no), `ReadOnlyPaths=`, `BindPaths=`, `TemporaryFileSystem=`, `RootDirectory=` and `RootDirectoryStartOnly=`, the `Private…=` namespaces, `ProtectHostname=`, `NoNewPrivileges=`, the capability sets, `SecureBits=`, `SystemCallFilter=` and `SystemCallErrorNumber=`, `SystemCallArchitectures=native` as `Config` fields resolved into one `SandboxValue` added to `Config.Spec`'s, a user namespace implied for an unprivileged supervisor; `+` unconfined, `!` the sandbox kept; the store's key holding the sandbox; no uid-0 `MAINPID=` for a sandboxed unit; a sandbox's failure `START_FAILED` wrapping `SANDBOX_FAILED` (`exit-code`) or `SANDBOX_UNAVAILABLE` (`resources`); amended 2026-11-08 a seventh time (kitsunium/sdk#738, S0h): the supervisor's hand-over — `Freeze`, `Reexec` through `syscall.Exec` of the program's `O_PATH` descriptor (`execveat` replaced for the runtime's exec lock), `TakeHandover`, `Resume`, codes `0.3.105.12`–`14`, BSD, macOS and Windows refused, a refused hand-over stopping every child in order (no adoption), no migration while one version exists
- ADR 0215 — a cgroup's BPF programs are built in Go, in a quarantined vendor module, accepted on 2026-11-08 (kitsunium/sdk#731, S6): `core/proc/bpf`, `service/proc/bpf` and `pkg/v1/proc/bpf` stay stdlib, the backend is `third-party/ebpf/cgroup` over cilium/ebpf, registered by a blank import — `docs/adr/0215-a-cgroups-bpf-programs-are-built-in-go-in-a-quarantined-vendor-module.md`
- ADR 0216 — a pool bounds workers, a semaphore bounds weight, and the ring takes many producers, accepted on 2026-11-03 (kitsunium/sdk#804, #805, #810, #812, #817, #1034, #1040, #813): `kernel/concur/pool` (published as `pkg/v1/concur/pool`, `0.1.6.*`), `kernel/concur/sema` (`0.1.7.*`), `ring.NewMulti`, `buffer.GetSize`'s size classes, `batcher`'s lifetime context and `FlushBackground` (`0.1.5.3`), `singleflight`'s shared context rooted in its call record (one allocation fewer); amends ADR 0006, 0010, 0014, 0049 and 0159
- ADR 0217 — a spawn gives the child only what it names, and waits a bounded time, accepted on 2026-11-03 (kitsunium/sdk#928, #929, #931, #933, #934, #935, #940, #943, #1088, #1089, #1090, #1091): the supervisor's descriptors sealed close-on-exec before every fork (`Spec.FDs = FDsInherited` opts out), `Spec.DrainGrace` bounding `Wait`'s copier join (`STDIO_DRAIN_TIMEOUT` `0.2.6.24`), the `ContextWaiter`, `ProcessHandle` (`HANDLE_UNAVAILABLE` `0.2.6.25`) and `Terminal` (`PTY_FAILED` `0.2.6.26`) sibling ports, `DieWithParent`, `Scheduling`, `ExitValue.CoreDumped` and `Usage`, a cgroup placement by `CLONE_INTO_CGROUP`; amends ADR 0016 and 0018; amended 2026-11-08 (#728, S2): `Spec.Sandbox`, a trampoline in two stages — the clone's namespaces, then in the child on one locked thread mounts, `pivot_root`, the bounding set, the identity, the capabilities, `no_new_privs`, Landlock and the seccomp filter last — from a sealed memfd payload, `SANDBOX_FAILED` `0.2.6.30`, `SANDBOX_UNAVAILABLE` `0.2.6.36`, `LandlockABI`, `CapSandbox`; amended again the same day: `MountTmpfsSticky`, a tmpfs of mode 1777 for `PrivateTmp=`, and (#738) a name `Spec.Env` repeats reaches the child once, with its last value, as `os/exec` reads it — `docs/adr/0217-a-spawn-gives-the-child-only-what-it-names-and-waits-a-bounded-time.md`
- ADR 0218 — a call over the private socket is framed, bounded and typed, accepted on 2026-11-07 (kitsunium/sdk#930): `proc/ipc/rpc` — `internal/core/proc/ipc/rpc` (the `Handler`, `Router`, `Request` and `Stream` ports, range `0.3.106.*`), the engine `internal/service/proc/ipc/rpc` and the facade `pkg/v1/proc/ipc/rpc`; a length past `MaxFrameBytes` refused before anything is allocated, a frame whole within `FrameTimeout` on the injected clock, `MaxInFlight` calls per connection — `docs/adr/0218-a-call-over-the-private-socket-is-framed-bounded-and-typed.md`
- ADR 0219 — a supervisor adopts, hears and hands over only what it can verify, accepted on 2026-11-07 (kitsunium/sdk#726, #729, #730, #739 — SB1, S3, S4, S5 of supervizio/agent#326): `Spec.Env` refusing `__KITSUNIUM_SDK_PROC_*` (`RESERVED_ENV` `0.2.6.31`), `exec.Attach` / `AttachPIDFile` adopting through a pidfd from a PID file walked safely, trusted only when root or this user made every step, else bounded by an `AttachValue` (`PID_FILE_REFUSED`, `ADOPTION_REFUSED`, `STATUS_UNKNOWN`, `0.2.6.32`–`34`), the sd_notify listener reading descriptors, `BARRIER=1`, `MAINPIDFD=1` and the sender's uid with every assignment bounded, `Spec.PIDEnv` giving a child its own `LISTEN_PID`, `systemd/listen`'s `PrepareFiles` and `CheckName` (`INVALID_FD_NAME` `0.2.6.35`); amends ADR 0016 and 0018; amended 2026-11-08: adoption on darwin and the BSDs through two kqueue knotes, a signal by pid only while one is silent, the process record decoded by `proc/internal/bsdabi`; the notify listener on FreeBSD through `LOCAL_CREDS_PERSISTENT` (`SCM_CREDS2`) — `docs/adr/0219-a-supervisor-adopts-hears-and-hands-over-only-what-it-can-verify.md`
- ADR 0221 — an HTTP/2 connection is frames, HPACK and a state machine on bytes, accepted on 2026-11-09 for kitsunium/sdk#959 and #960, revisable while v0 (ADR 0040): amends ADR 0186 §D6 and §Deferred — the frame layer (`ParseFrameHeader`, the `Append` family) and an in-house HPACK on `[]byte` in `internal/service/net/http2`, its values and codes `0.3.109.*` in `internal/core/net/http2` (`FrameValue`, `LimitsValue` — ADR 0195 §D3's numbers —, `ErrCode`); `Decoder` a pointer-free value over caller-given table memory, a list ceiling charged before any copy or insert (RFC 7541 §7.3's bomb), two refusal classes by whether the table survives; `Encoder` static-table names and never-indexed literals; one connection's streams, windows and SETTINGS as a pointer-free state machine; `pkg/v1/net/http2` comes with the adapter (#961) — `docs/adr/0221-an-http2-connection-is-frames-hpack-and-a-state-machine-on-bytes.md`
- ADR 0222 — a loop runs on kqueue on darwin and the BSDs, beside epoll, accepted on 2026-11-11 (kitsunium/sdk#976), revisable while v0 (ADR 0040): resolves ADR 0186 §D5's deferral — `LoopBackendKqueue` and the engine token `kqueue` at epoll's rank, switched off apart; a level-triggered driver on the shared core, a staged probe, the Linux-only rows absent; kqueue opt-in (`LoopBackendKqueue`) until the paired gate holds, `auto` portable there (amended the same day); the backend-neutral conformance cases, the drain and the idle-memory bound on kqueue; the macos-15 paired gate next — `docs/adr/0222-a-loop-runs-on-kqueue-on-darwin-and-the-bsds-beside-epoll.md`
- ADR 0230 — a segmented log is a kernel primitive that writes, and never decides, accepted on 2026-11-12 (kitsunium/sdk#820): `kernel/fs/seglog` (`0.1.8.*`), the consensus file log's mechanism without its vocabulary; amends ADR 0159; amended for `Rewrite` (#824) — `docs/adr/0230-a-segmented-log-is-a-kernel-primitive-that-writes-and-never-decides.md`
- ADR 0231 — a record is kept until its retention ends, and every group reads it at its own pace, accepted on 2026-11-12 (kitsunium/sdk#818, milestone 1 of #890): the `data/stream` domain, `Stream` frozen at four methods with five siblings, `0.2.38.*` and `0.3.94.*`; amended for the file engine (#821), the SQL engine (#822), the subscriber (#823) and compaction by key (#824) — `docs/adr/0231-a-record-is-kept-until-its-retention-ends-and-every-group-reads-it-at-its-own-pace.md`
- Layer placement audit — `.claude/contexts/sdk-layer-placement-audit.md`
- Bazel adoption context — `.claude/contexts/bazel-9-go-sdk.md`
