<!-- updated: 2026-10-03T06:30:00Z -->
# internal/service/

The long rows of the Contents table moved, unchanged and under the same section title, to
`internal/service/NOTES.md` (ADR 0253): read it before editing a package whose row is not below.

## Purpose

Concrete implementations of the contracts declared in `internal/core/*`. This is where actual I/O, formatting, locking, codec dispatch, and error wrapping happen. The `pkg/v1/*` facade wraps service constructors and hides the wiring from consumers.

## Contents

| Sub-tree | Purpose | Code prefix |
|---|---|---|
| `data/codec/` | 16 wire-format codecs over 24 Format names (asn1, baseenc[9], bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml), each implementing `core/data/codec.Codec`; all satisfy `Appender`, most also `StreamingCodec` | `0.3.2.*` … `0.3.41.*` (one PP slot per codec) |
| `app/id/` | identifier generators (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID) implementing `core/app/id.Generator`; stdlib-only, cross-OS, self-registered except TypeID (ADR 0024; the exception is ADR 0038) | `0.3.39.*` (declared in `core/app/id`, ADR 0160) |
| `app/resilience/` | concrete reliability policies (retry/circuit-breaker/rate-limit, single or keyed per caller/bulkhead/timeout/fallback/hedging) implementing `core/app/resilience.Runner`, plus `BackoffValue`, the alias of the `kernel/backoff` curve the retry waits — compared against its bound before any conversion, so it never wraps negative — and a retry that waits on an injected `clock.Timed`; stdlib + kernel `backoff`/`clock`, cross-OS (ADR 0026, ADR 0103) | (emits core sentinels `0.2.8.*`) |
| `app/config/` | env+file sources, merge+decode+validate `Load[T]`, cross-OS poll watcher, implementing `core/app/config`; codec-dispatched file parse plus the compiled schema `NewSchemaValue`/`LoadSchema` — required keys, typed defaults, a closed vocabulary (ADR 0061) | (emits core sentinels `0.2.10.*`) |
| `app/scheduler/` | five-field POSIX cron parser + fixed-interval `Every` + the firing engine, implementing `core/app/scheduler`; waits through `kernel/clock.Timed`, never package `time` (ADR 0041) | `0.3.43.*` (the parser) and `0.2.12.*` (the engine), both declared in `core/app/scheduler` (ADR 0160) |
| `app/validation/` | the constraint engine implementing `core/app/validation`: built-in constraints, the reflection-free `Field`/`Each` combinators, and the `Struct[T]` struct-tag front end whose compiled plan is cached per `(type, mode)` — 74× cheaper than recompiling, see its `BENCH.md`; every dialect construct the SDK declines is refused BY NAME at compile time (ADR 0046) | `0.3.47.*` and `0.2.15.*`, both declared in `core/app/validation` (ADR 0160) |
| `app/health/` | the check registry, per-check timeouts, panic recovery, staleness bound, the drain latch and the HTTP handler, implementing `core/app/health` (ADR 0060), and `Ask` — the client half of a readiness probe, what a container's HEALTHCHECK runs: loopback for an unspecified listen host, no proxy, no redirect, a bounded drain, the budget on the injected clock (ADR 0131) | `0.3.59.*` (declared in `core/app/health`, ADR 0160) |
| `proc/ipc/` | a private socket between processes of one machine, the engines behind the `Listener` and `Dialer` ports of `core/proc/ipc` (ADR 0160): the directory gates everywhere (`0700`, the DACL on Windows) and so does the path to it, walked with `kernel/fs/pathchain` (`PATH_UNSAFE`: a link, another account's component or a replaceable one where anybody can write), `SO_PEERCRED` names the peer on Linux, a live socket is never taken over and a client refuses a foreign one before it speaks (ADR 0148) | `0.3.91.*`, declared in `core/proc/ipc` (ADR 0160) |
| `proc/ipc/rpc/` | a framed, bounded, codec-typed call over the private socket (ADR 0218): `Server` over the `Listener` port, `Client` over the `Dialer` port (`Call`, `Stream`, `Ping`); a length past `MaxFrameBytes` refused before anything is allocated, `FrameTimeout` from a frame's first byte on the injected clock, `MaxInFlight` handlers per connection (`OVERLOADED` at once, `MaxInFlight + 2` goroutines), budgets and cancel frames | `0.3.106.*` (in `core/proc/ipc/rpc`) |
| `app/mail/spool/` | the durable mail outbox over `queue` (file with a directory, memory without): `Send` validates and stamps a Message-ID every retry keeps; a failed attempt is PARKED by extending its lease `Backoff.Delay(attempt)`, only the last attempt nacks into a dead letter carrying the true cause; a delivered mail's redelivery is dropped (ADR 0111). `SendWithID` queues under an identifier the caller minted — a dot-atom of at most `MaxIDBytes`, `INVALID_MAIL_ID` otherwise — and a repeated one meets the same ledger (ADR 0141) | `0.3.81.*` (declared in `core/app/mail/spool`, ADR 0160) |
| `data/codec/strictjson/` | the strict, bounded JSON decoder — not a codec Format — on `encoding/json/v2` with `RejectUnknownMembers`; a reader that hands out the bound plus one byte, a reader verdict taken before the decoder's, refusals that keep no decoder error in the chain; `DecodeRequest` over `http.MaxBytesReader`, with the empty body and the media type settled first, is `strictjson/httpbody`'s, so the decoder links no `net/http` (ADR 0102) | `0.3.72.*` (declared in `core/data/codec/strictjson`) |
| `observe/profiling/` | the process's own profiles on `runtime/pprof`: a bounded CPU window and the live heap, `Parse` from profile.proto with a stdlib wire reader (strict, bounded, checked against Google's decoder, fuzzed), `Fold` onto owners the caller's function names with exact sums, the goroutine dump read — labels in either printed form — and grouped, `CanonicalName`; its values and codes are `core/observe/profiling`'s since ADR 0160, and it has no port — the attribution is a parameter (ADR 0121) | `0.3.89.*` — declared in `core/observe/profiling` |
| `data/codec/jsonshape/` | a Go type's wire shape under `encoding/json` — not a codec Format: kinds, members resolved by json/v2's field walk under the v1 options Go 1.27 builds `encoding/json` with (promotion, dominance, the tag grammar, the `embed` option), optionality and nullability as encoding decides them, opaque marshalers, and each member's Go field (tag, Go name, index path) for a framework's own tags (ADR 0133) | none |
| `data/codec/jsonpatch/` | two JSON documents' difference as RFC 6902 operations — not a codec Format: add / remove / replace at an RFC 6901 pointer with the value written and the value replaced, documents read strictly with `encoding/json/jsontext` and compared as RFC 6902 §4.6 compares values (numbers exactly, as sign, digits and a power of ten), arrays aligned by their longest common subsequence within a bound, then paired (ADR 0143) | `0.3.90.*` |
| `data/codec/jcs/` | RFC 8785 canonical JSON — not a codec Format: `Canonicalize(doc)` and `Marshal(v)` write one byte form for signing or hashing (members sorted by UTF-16 code units, ECMAScript numbers, minimal string escapes), I-JSON read strictly with `encoding/json/jsontext`; `crypto/key/jwk`'s thumbprint input (#895) | `0.3.102.*` |
| `data/codec/jsonschema/` | a JSON Schema 2020-12 validator for documents somebody else wrote — not a codec Format: `NewValidator(schema, Config)` compiles the core, applicator and validation vocabularies (`$ref` never fetched, numbers exact, formats asserted on request; the dynamic scope, `$vocabulary`, the unevaluated and content vocabularies refused by name), `Validate(doc, maxBytes)` reads through strictjson and returns a `validation.ReportValue` (#882, ADR 0210) | `0.3.103.*` |
| `net/static/` | an `http.Handler` serving an `io/fs.FS`: the name cleaned from the root, no directory listing, an optional single-page fallback for extension-less routes only, the security headers on every response, immutable caching for names the caller marks content-hashed, web types pinned against the host's MIME tables, a 404 for every refusal of a NAME and a 500 for the tree's own failures (ADR 0130) | none (core sentinel `0.2.11.34`) |
| `proc/buildinfo/` | what the running binary was built from: `ReadBuild`/`ParseBuild` over `debug.BuildInfo` with a module's release, commit and local directory kept apart — split from `proc/self` so a reader of its build imports no runtime/metrics (ADR 0172) | (none) |
| `proc/self/` | what the running process is doing: `ReadStats` over `runtime/metrics`, `ReadGCStats` and `getrusage` — the runtime's estimate, flagged, where there is none (ADR 0100) | (none) |
| `observe/logger/internal/logfile/` | the hardened open both file sinks share (`observe/logger/sink/file`'s `New`, `observe/logger/writer/rotfile`'s every reopen), Go-internal to the logger's subtree, where both sit: an `os.Lstat` refusal of a symlinked final component, then `O_APPEND\|O_CREATE\|O_WRONLY` plus `O_NOFOLLOW` on every Unix, both refusals carrying `path` and `kind=symlink` (CWE-59). It owns NO code: each sink hands it a `RefusalSpec` with its own wraps | none — the calling sink's (`0.3.14.*`, `0.3.27.*`) |
| `proc/internal/rlim/` | the one constructor of the kernel's resource-limit struct `proc/exec`'s trampoline and `proc/rlimit` share: `Make(soft, hard)` → `syscall.Rlimit`, split by build tag because FreeBSD and DragonFly declare its fields `int64` where every other Unix declares `uint64`, so `LimitInfinity` lands as `RLIM_INFINITY` on both. Go-internal to the proc family, where both sit | none |
| `data/transform/` | the stdlib compressors behind `core/data/transform` — gzip, raw DEFLATE (`flate`) and the RFC 1950 `zlib` envelope — in one package that self-registers all three (ADR 0014) | `0.3.26.*`, declared in `core/data/transform` beside its `0.2.5.*` (ADR 0160) |
| `observe/logger/writer/` | the log-transport writers behind the `core/observe/logger/writer` registry (ADR 0012, ADR 0015): `console`, `file`, `rotfile`, `journald` and `nettransport` register a factory; `dbsink` is the driver-agnostic database sink shell; `levelgate` is the severity floor a writer's sink is wrapped in (ADR 0132) | rotfile `0.3.27.*`, nettransport `0.3.30.*`, journald `0.3.31.*` — declared in `core/observe/logger/writer/*` since ADR 0160; the others own none |
| `net/` | the network domain's engines (ADR 0029): `client` (the policy-enforcing outbound transport), `server` (one listener engine for TCP, Unix, TLS and mutual TLS), `sse` (Server-Sent Events), `websocket` (RFC 6455, ADR 0047), `tlsid` (reads TLS material from disk and nothing else) and `static` (its own row above) | (emit core sentinels `0.2.11.*`) |
| `proc/` | OS process supervision (ADR 0016): `exec` (spawn under explicit credentials, in its own process group), `childwait` (a child's status handed to the `Process` that owns it — ADR 0093), `reaper`, `signal`, `rlimit`, `cgroup`, `memlimit` (ADR 0075), `systemd/notify` and `systemd/listen` (sd_notify and socket activation), `self` and `ipc` (their own rows above) | (emit core sentinels `0.2.6.*`) |

A family that holds several domains is a directory with no Go code of its own
and a `CLAUDE.md` naming its members (ADR 0155 §2): `crypto/` groups its
schemes by role, `security/` holds `authz`, `redact`, `secret`, `session`
and `token`, at the same paths as their facades under `pkg/v1` and their
contracts under `internal/core` — `redact`'s since ADR 0160 — `net/` holds
the network domain's six engines — `server`, `client`, `tlsid`, `sse`,
`websocket`, `static` — under the one contract `internal/core/net`, with their
facades at the same paths under `pkg/v1/net`, and `proc/` holds the
process-supervision engines over the one contract `internal/core/proc`, the
two systemd protocols under `systemd/` and the private socket `ipc`, with
their facades under `pkg/v1/proc` — at the same paths, except that `exec` and
`self` are both published by `process` and `childwait` by none — and the
constructor `exec` and `rlimit` share, Go-internal to the family at
`proc/internal/rlim`. `observe/`
holds the three signals and the profiles a process reports about itself —
`logger` (its writers beneath it at `logger/writer`), `metrics`, `trace` and
`profiling` — over the contracts under `internal/core/observe`, with their
facades at the same paths under `pkg/v1/observe`; the two helpers shared
inside the family sit beside their importers, where Go's `internal/` rule
confines them —
`observe/internal/otlp` (both signals' wire) and
`observe/logger/internal/logfile` (both file sinks' open). `data/` holds the
engines that encode, store and move a caller's data — `codec` (every wire
format, and the JSON tools that are not Formats beneath it), `transform`,
`sql`, `docstore`, `queue`, `cache` and `vfs` — over the contracts under
`internal/core/data`, with their facades at the same paths under
`pkg/v1/data` — `transform`'s too, which `codec`'s compression frame goes
through.
`app/` holds the mechanisms an application is assembled from — `config`,
`cli`, `i18n`, `validation`, `view`, `events`, `scheduler`, `statemachine`,
`resilience`, `lifecycle`, `health`, `lock`, `id`, `forward` and `mail`, with
the mail outbox beneath it at `mail/spool` — over the contracts at the same paths under
`internal/core/app`, with their facades at the same paths under `pkg/v1/app`,
except that `mail/spool` is published by `mail`. No domain sits at the root
of this layer any more.

The distribution domains' engines — `entitlement`, `selfupdate`, `gate` and
`vcs/git` — left this layer for the framework (ADR 0158):
`framework/internal/service/<domain>`, over `pkg/v1` alone. Their ranges
`0.3.66.*` and `0.3.67.*` kept their values there (ADR 0160).

## Module

A directory of the `github.com/kitsunium/sdk/internal` module (ADR 0171), whose `go.mod` is at `internal/` and shared by the three layers — a module of its own until ADR 0162, which needed `replace` directives to reach `../kernel` and `../core`. `cd internal/service && GOWORK=off go build ./...` builds this layer's packages.

## Conventions

- **Imports allowed**: stdlib + `internal/kernel/*` + `internal/core/*` + **other `internal/service/*` packages**. The internal module requires no module outside the standard library: every codec is written on it (ADR 0156), the yaml.v3 reader is the opt-in `third-party/codec/yaml` module, and `golang.org/x/mod` gave way to `internal/kernel/semver`; a vendor library is a `third-party/` module of its own (ADR 0157). Never `pkg/*`. A service package composing a sibling is permitted by the `service_consumers` group in `internal/service/BUILD.bazel`, which lists `//internal/service/...`, and several domains are built on it by decision rather than by accident — `queue` composes `service/data/vfs` for its atomic, durable writes (ADR 0054 §D7), `token` composes `service/crypto/key/jwk`, `lifecycle` delegates signals and `sd_notify` to `service/proc`. This line used to omit the sibling case, and an automated review read the omission as a layer rule.
- **Concurrent safety**: every public type honours the "safe for concurrent use" contract inherited from the core interface it implements. Codec singletons are stateless; logger handlers serialise writes via `sync.Mutex` or async ring buffer.
- **Error wrapping**: `errs.Wrap(cause, WrapParams{…})` when the cause is a stdlib / third-party error; the `errs.WrapParams` fields are SILENTLY IGNORED when the cause is already an `*errs.Error` (origin wins). See `internal/kernel/errs/CLAUDE.md`.
- **Codec registration**: each codec exports `var Codec codec.Codec = codec.Register(&xxxCodec{})` at package load — no `init()`. Blank-importing the package is enough to make it resolvable by Name / MIME / Extension.
- **Function length**: `KTN-FUNC-MAXLOC` caps every function at 50 lines. Helpers are split out (e.g. `escapeFormulaCells` / `escapeFormulaRow` in `data/codec/csv`, `renderLine` / `writeLine` in `observe/logger`).

## Do NOT

- Let a service type reach a consumer by any road but `pkg/v1`. The public facade is `pkg/v1/*`: a service type a consumer holds is published there as an alias, and every public signature names it by that alias (`pkg/v1/CLAUDE.md` §Public surface contract, "Aliases, not new types"); an implementation this layer returns behind an interface stays behind it — the codec registry hands `svccodec.jsonCodec` back as a `codec.Codec`, and `Build` hands the recycled `chainBuilder` back as a `Builder` (`IFACE-PLUGIN`). Aliasing a service type is not the defect. Which types `pkg/v1` aliases onto this layer is the ownership question of ADR 0074 (`docs/adr/0074-what-a-public-alias-may-point-at.md`): one engine's handle, its `Option` closures, its construction parameters and a value only it produces belong here and are aliased here — "the common case and … not a defect" — and what ADR 0074 still refuses is a type a core port speaks that is declared in a service package. This line used to read "Re-export a service type as the public-facing API", written when `pkg/CLAUDE.md` had every public type alias core or kernel; once ADR 0029 §D1 and ADR 0074 made service aliases the rule, it read as a ban on them, and an automated review read it that way.
- Call `fmt.Errorf` / `errors.New` in production, or declare an error code in this layer. Every code and sentinel an engine raises is declared by the core package at the engine's path, in its `codes.go` / `errors.go` (ADR 0160 §2), and used from here through `errs.Wrap` (call sites in `codec.go` / handler code); `scripts/pre-commit/check-core-symmetry.sh` — `make lint` and CI — refuses an `errs.Define`, under any import name, or a `Code` constant in a production file of this layer.
- Reach into `core/*` structs to mutate them. Domain values (`AttrValue`, `RecordEvent`) are immutable after construction.
- Swallow a third-party encode/decode error silently. Wrap it via `errs.Wrap` so `errors.Is(err, originalCause)` keeps working and the dotted-quad code surfaces.
- Add an `init()` function to register a codec — the package-level `var Codec = codec.Register(...)` initialiser is the convention.

## Subtree

- `observe/` — see `internal/service/observe/CLAUDE.md` (the observe family's engines — `logger` and its writers, `metrics`, `trace`, `profiling` — the rule that put them together, and where the two shared helpers sit)
- `observe/logger/` — see `internal/service/observe/logger/CLAUDE.md` (full contract, error catalogue, output format)
- `app/mail/` — see `internal/service/app/mail/CLAUDE.md` (MIME composition, the SMTP policy above net/smtp, and why nothing is sanitised — ADR 0064)
- `data/codec/` — see `internal/service/data/codec/CLAUDE.md` (16 codec packages + per-codec error ranges)
- `crypto/` — see `internal/service/crypto/CLAUDE.md` (scheme packages grouped by role — `aead/`, `agree/`, `hash/`, `kdf/`, `key/`, `mac/`, `password/`, `sign/` — the keyenvelope/keytree compositions, the `jwk` key format, and the `commonpw` list)
- `net/` — see `internal/service/net/CLAUDE.md` (the net family's engines — `server`, `client`, `tlsid`, `sse`, `websocket`, `static` — the rule that put them together, and why none declares a code)
- `proc/` — see `internal/service/proc/CLAUDE.md` (the proc family's engines — the process-supervision primitives, `systemd/{notify,listen}` and `ipc` — the rule that put them together, and why none declares a code)
- `security/` — see `internal/service/security/CLAUDE.md` (the security family's engines — `authz`, `redact`, `secret`, `session`, `token` — the rule that put them together, and why the schemes they compose stay in `crypto/`)
- `data/` — see `internal/service/data/CLAUDE.md` (the data family's engines — `codec`, `transform`, `sql`, `docstore`, `queue`, `cache`, `vfs` — the rule that put them together, and which of them compose which)
- `app/` — see `internal/service/app/CLAUDE.md` (the app family's engines — `config`, `cli`, `i18n`, `validation`, `view`, `events`, `scheduler`, `statemachine`, `resilience`, `lifecycle`, `health`, `lock`, `id`, `mail` with `mail/spool` — the rule that put them together, and which of them compose which)
- `app/id/` — see `internal/service/app/id/CLAUDE.md` (UUIDv4/v7, ULID, snowflake, NanoID, KSUID, TypeID — ADR 0024, ADR 0038)
- `app/i18n/` — see `internal/service/app/i18n/CLAUDE.md` (the thirteen-language table, the load-time refusal, and the negotiator that cannot fail — ADR 0063)
- `app/resilience/` — see `internal/service/app/resilience/CLAUDE.md` (retry/breaker/ratelimit/bulkhead/timeout/fallback/hedging — ADR 0026)
- `observe/metrics/` — see `internal/service/observe/metrics/CLAUDE.md` (in-memory meter + the text exporter, the lossy Prometheus connector and the lossless OTLP/JSON wire — ADR 0027 / ADR 0044 / ADR 0048)
- `app/config/` — see `internal/service/app/config/CLAUDE.md` (env+file loader + schema + poll watcher — ADR 0028 + ADR 0061)
- `app/scheduler/` — see `internal/service/app/scheduler/CLAUDE.md` (cron parser + engine — ADR 0041)
- `app/lifecycle/` — see `internal/service/app/lifecycle/CLAUDE.md` (the three mechanics behind the partial-start unwind, what an expired budget does and deliberately does not do, and the opt-in `Run` — ADR 0050)
- `security/token/` — see `internal/service/security/token/CLAUDE.md` (JWT + PASETO v4.public, the RFC 8725 coverage table, and what each algorithm and each refusal costs — ADR 0042)
- `app/validation/` — see `internal/service/app/validation/CLAUDE.md` (constraints, combinators, the struct-tag plan cache — ADR 0046)
- `data/vfs/` — see `internal/service/data/vfs/CLAUDE.md` (the five publication steps, what atomicity does and does not reach, and why Windows is refused rather than approximated — ADR 0056)
- `app/view/` — see `internal/service/app/view/CLAUDE.md` (the html/template engine, the escaping probe, the type-gated trust scan and the parse-once rule — ADR 0058)
- `security/secret/` — see `internal/service/security/secret/CLAUDE.md` (the three stores, the keyring's box format, the rotator's rules — ADR 0096; subject keys, their box, their cache and the erasure — ADR 0142)
- `security/session/` — see `internal/service/security/session/CLAUDE.md` (memory + file stores, the sealer, and the platform matrix — ADR 0045)
- `data/sql/` — see `internal/service/data/sql/CLAUDE.md` (the savepoint traps, the detached-cleanup rule, the per-engine migration guarantee — ADR 0055; Join and Defer — ADR 0139; SQLite's file lock — ADR 0140)
- `app/lock/` — see `internal/service/app/lock/CLAUDE.md` (both lockers, the measured `flock` intra-process behaviour that forces the in-process gate, the platform matrix, and why one backend expires and the other does not — ADR 0052)
- `observe/trace/` — see `internal/service/observe/trace/CLAUDE.md` (tracer, samplers, recorder, the OTLP/JSON wire and the two middlewares — ADR 0051)
- `observe/internal/otlp/` — see `internal/service/observe/internal/otlp/CLAUDE.md` (the OTLP transport and wire both signals share, and the `SignalSpec` seam that keeps each signal's codes)
- `observe/logger/internal/logfile/` — see `internal/service/observe/logger/internal/logfile/CLAUDE.md` (the hardened open both file sinks share, its platform table, and the one O_NOFOLLOW proof)
- `data/cache/` — see `internal/service/data/cache/CLAUDE.md` (the tagged memory store, the four removal paths, the chain's ordering rules, and why stampede protection stops at the process boundary — ADR 0049)
- `app/cli/` — see `internal/service/app/cli/CLAUDE.md` (the one FlagSet constructor, the generated help, and the Visit-not-VisitAll rule — ADR 0065)
- `security/authz/` — see `internal/service/security/authz/CLAUDE.md` (RBAC, ABAC, the single combiner, and why every refusal says the same sentence — ADR 0057)
- `app/events/` — see `internal/service/app/events/CLAUDE.md` (why registration is a function and not a method, why `kernel/concur/snapshot` and not a broadcast topic, and what a listener panic does — ADR 0053)
- `data/queue/` — see `internal/service/data/queue/CLAUDE.md` (rename(2) as the whole exclusion, the SIGKILL test, what durability costs — ADR 0054 — the wake an idle consumer sleeps on — ADR 0104 — and the SQL broker, the growing retry delay and the dead letters' decisions — ADR 0151)
- `data/docstore/` — see `internal/service/data/docstore/CLAUDE.md` (the snapshot and its overlay, why replay is idempotent, the two locks, the two publication verdicts — ADR 0110; the SQL engine's tables, statements and refusals — ADR 0139; the versions, in the document's own write — ADR 0143)
- `app/forward/` — see `internal/service/app/forward/CLAUDE.md` (why a batch is retried in the handler and not nacked, the age as budget, what the counts can and cannot see — ADR 0185)
- `app/mail/spool/` — see `internal/service/app/mail/spool/CLAUDE.md` (why a retry parks instead of nacking, the Message-ID minted at Send, the one duplicate left — ADR 0111; the identifier a caller mints, the rule it keeps and what a repeated one does — ADR 0141)
- `security/redact/` — see `internal/service/security/redact/CLAUDE.md` (the three rules, why the writer is the package's own, scrub before cut — ADR 0101)
- `proc/ipc/` — see `internal/service/proc/ipc/CLAUDE.md` (the two gates, dial-then-remove — ADR 0148; the ports its engines implement — ADR 0160)
- `proc/ipc/rpc/` — see `internal/service/proc/ipc/rpc/CLAUDE.md` (the wire, the keeper that holds every deadline of a connection, why an answer and its slot's release share the write lock — ADR 0218)
- `data/codec/strictjson/` — see `internal/service/data/codec/strictjson/CLAUDE.md` (the bound on reading, the reader's verdict first, no decoder error kept — ADR 0102)
- `data/codec/strictjson/httpbody/` — see `internal/service/data/codec/strictjson/httpbody/CLAUDE.md` (the three rules only an HTTP body adds, and why they are not in the decoder)
- `app/statemachine/` — see `internal/service/app/statemachine/CLAUDE.md` (flights instead of waits, guards on the loop's goroutine, one transition per entity per run, lazy deletion — ADR 0120)
- `observe/profiling/` — see `internal/service/observe/profiling/CLAUDE.md` (the stdlib decoder, the runtime's one CPU profiler, a dump parser that never fails — ADR 0121)
- `data/codec/jsonshape/` — see `internal/service/data/codec/jsonshape/CLAUDE.md` (the engine it follows, the promotion and dominance rules, what Optional means — ADR 0133)
- `data/codec/jsonpatch/` — see `internal/service/data/codec/jsonpatch/CLAUDE.md` (RFC 6902 equality, exact numbers, arrays aligned then paired, indices as applied — ADR 0143)
- `data/codec/jcs/` — see `internal/service/data/codec/jcs/CLAUDE.md` (RFC 8785; a policy over JSON like strictjson — ADR 0102, amendment of 2026-11-03)
- `data/codec/jsonschema/` — see `internal/service/data/codec/jsonschema/CLAUDE.md` (the official suite vendored as testdata — ADR 0210)
- `net/static/` — see `internal/service/net/static/CLAUDE.md` (the cleaned name, 404 for the name and 500 for the tree, the relative redirect, the pinned types — ADR 0130)
- `proc/buildinfo/` — see `internal/service/proc/buildinfo/CLAUDE.md` (the build of the running binary, apart from runtime/metrics — ADR 0100, ADR 0172)
- `proc/self/` — see `internal/service/proc/self/CLAUDE.md` (runtime state of the running process — ADR 0100)
- `app/health/` — see `internal/service/app/health/CLAUDE.md` (the registry, the drain latch, the handler and `Ask` — ADR 0060, ADR 0131)
- `data/transform/` — see `internal/service/data/transform/CLAUDE.md` (the three stdlib compressors and the bounded decompression — ADR 0014)
- `net/{client,server,sse,tlsid,websocket}/`, `proc/{cgroup,childwait,exec,memlimit,reaper,rlimit,signal}/`, `proc/systemd/{listen,notify}/`, `observe/logger/writer/{console,dbsink,file,journald,levelgate,nettransport,rotfile}/` — each directory's own `CLAUDE.md`

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/service/...

# Fallback (go test)
cd internal/service
GOWORK=off go test -race -cover ./...
```
