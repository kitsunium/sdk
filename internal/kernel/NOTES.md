# internal/kernel/ — reference

Moved unchanged from `internal/kernel/CLAUDE.md` (ADR 0253); each section keeps its title.

## Audit: who stays, who would leave

As of the 2026-04-19 audit (extended by ADR 0006 to admit `ring`):

- `errs` stays — every layer of the SDK uses typed errors, including kernel itself. Meta-infrastructure.
- `clock` stays — textbook generic time abstraction. Extended in place (2026-09) with the waiting half (`Waiter`/`Timer`/`Ticker`) plus `ManualClock`, which is still stdlib-only and still domain-free: no `Job`, no `Task`, no `Schedule` appears in a signature. `Clock` itself was deliberately NOT widened — it is reachable downstream through the `pkg/v1/data/cache.Config` alias, so a new method would break every consumer's hand-written double. See `clock/CLAUDE.md` §"Why `Clock` was NOT extended".
- `recycler` admitted by ADR 0010 — `Pool[T]` + `CappedPool[T]` are textbook generic object pools (reuse + reset + cap-discard). Any byte buffer, codec stream, HTTP body encoder, or metrics line writer can reuse the mechanism; the thresholds stay with the consumers.
- `buffer` stays — the `[]byte` pool is generic even though `service/observe/logger` is the heaviest consumer today. Since ADR 0010 it is a thin specialisation over `recycler.CappedPool[*[]byte]` (the generic `Pool[T]` moved to `recycler`).
- `ring` admitted by ADR 0006 — SPSC bounded queue is a textbook generic primitive. Logger's async middleware is the only consumer today; metrics batchers and codec stream pipelines are obvious future users.
- `snapshot` admitted by ADR 0011 — `Value[T]` is a textbook generic copy-on-write container (lock-free `Load` + mutex-serialised writers). The codec registry consolidated its three hand-rolled `atomic.Pointer[map]` + CAS loops onto it; routing tables, feature-flag maps, and hot-reloaded config are obvious future consumers.
- `singleflight` admitted by ADR 0049 — `Group[K,V]` is textbook generic call deduplication (`Do`/`Forget`/`InFlight`; no domain word in any signature). It ships with **one** in-tree consumer, `internal/service/data/cache`, and that is recorded rather than dressed up: the second consumer claimed during planning (`config`) was checked and does not exist — `config.Load` is stateless and `pollWatcher.Watch` delegates the reload to a caller callback, so there is nothing to deduplicate. Two real candidates exist and were left alone (`service/app/validation.planFor`, `service/data/codec/tlv.cachedStructTypeInfo`, both `LoadOrStore` on a compile-once cache that accepts the duplicate in writing). The admission rests on the rule that governs — stdlib-only AND generic — and on the precedent in the row above it: **ADR 0025 admitted `cache` with ZERO domain consumers**; today `internal/service/data/cache` and `internal/service/security/secret` build on it. A consumer count was never the bar; ADR 0010's "three copies already existed" was a *consolidation* argument, not a gate.
- `plugin` admitted by ADR 0071 — `Unusable(v any) (why string)` names no domain and knows no port; what it judges is the VALUE, and the two shapes it refuses are properties of Go's interfaces, not of any registry. It has **fifteen** in-tree consumers on day one, in eight core packages, which is unusual here and is the consolidation argument ADR 0010 made for `recycler`: the guard existed fifteen times as `if x == nil` and was wrong in the same way fifteen times. `Registry[K cmp.Ordered, V comparable]` joined it on the same argument: the name-keyed copy-on-write table behind a registry (a check-and-publish under `snapshot.Value`'s writer lock, idempotent on the identical value, a conflict REPORTED as a bool for the registrar to refuse with its own code) existed as a private copy per registry; it names no domain and builds no error. Its first users were the metrics and trace exporter registries; the six core registries that each carried a copy — codec, writer, crypto, transform, id, view — followed (ADR 0159), and `Claim` (the holder of a taken name, reported by the step that refused) was added for the two needs `Publish`'s verdict could not serve: codec's refusal of every second registration of a `Format`, and its alias conflicts naming their owner.
- `backoff` admitted on rule 1 — stdlib-only, and its signatures name a
  duration, a factor, a ceiling and a jitter, nothing else — and moved here
  from `service/app/resilience`, where ADR 0103 had published it. It is the
  consolidation argument again: five service domains (`lifecycle`, `queue`,
  `statemachine`, `mail/spool`, `net/server`) imported the whole `resilience`
  package for this one type, which made a reliability-policy package the hub
  of the service graph. They import the kernel now, and the two aliases that
  keep the published names point at it (ADR 0074).
- `flock` admitted on rule 1, and as a CONSOLIDATION (ADR 0159 §3): its
  signatures name a file and nothing else, and the non-blocking `flock(2)` it
  holds existed twice, line for line — in `service/app/lock`, with the
  `LockFileEx` half ADR 0081 added, and in `service/security/session` (ADR
  0073). Both call it now and keep what is theirs: lock its gate, its fencing
  ledger and the pairing of the lock with its hardened open (a test fails if
  `flock.Native` and lock's `nofollow_*.go` tag sets part); session its gate,
  its abandonable waits and a platform gate NARROWER than `Native` (Windows has
  the lock and not the owner-only modes). It never blocks, because a blocking
  call parks a thread no cancellation reaches; it promises nothing about
  goroutines sharing one descriptor, because the two kernels answer that
  oppositely.
- `winacl` admitted on rule 1: stdlib-only — two `advapi32` exports through
  `syscall.NewLazyDLL`, no `x/sys` — and its signatures name a directory and a
  rights mask. It is the DACL reader `service/app/lock` wrote (ADR 0084, 0086)
  and EXPORTED for `service/data/queue` (ADR 0095), which made the queue import
  the lock service for one Windows function — a service-to-service edge the
  kernel removes. Like `pathchain` it produces no verdict: the lock accepts a
  list it could not read and the queue refuses one, each for a reason the other
  does not have, and the reader names "could not look" so both can choose.
- `pathchain` admitted on rule 1: stdlib-only, and its signatures name a path
  and its components — no lock, no directory role, no policy. It shipped with
  one in-tree consumer, `internal/service/app/lock`, and that was stated rather than
  dressed up; `internal/service/proc/ipc` is the second (its socket directory's
  parents) and `internal/service/security/session` the third (its file store's
  `Dir`), each with a policy of its own (ADR 0154, as built); three neighbours
  ask a related question with hand-rolled checks
  today (`internal/service/data/queue`'s symlinked state directory,
  `internal/core/data/vfs`'s `PathEscaped`, `internal/service/observe/logger/writer/rotfile`'s
  `refuseSymlink`) and none of them can ask THIS one. It deliberately produces
  no verdict: `/var/run` being a symbolic link is a distribution's decision and
  `/tmp/myapp` being one may be an attack, so the primitive measures and the
  domain decides.
- `semver` admitted on rule 1, and as a REPLACEMENT rather than a new
  capability: it takes the place of `golang.org/x/mod` (ADR 0156 §4), which
  was the one module outside the standard library the service layer required
  for something other than a codec. Its signatures take a version string and
  answer about it — no release, no update, no product — and its vocabulary is
  SemVer's and the Go toolchain's, which every Go program shares. It ships with
  one in-tree consumer, `internal/service/proc/self` (pseudo-versions); the two
  callers of `x/mod/semver`, `selfupdate` and `entitlement`, moved to the
  framework (ADR 0158) and reach it through `pkg/v1/data/semver`, the alias that
  exists for exactly that reason (ADR 0159 §4). Written from the
  specifications, not from x/mod's source; x/mod's test vectors are what the
  suite borrows, so the two are shown to agree.
- `swar` admitted on rule 1: stdlib-only, and its signatures name a slice, two
  bytes and a bound — no header, no escape, no token. It ships with no consumer
  in the tree yet, which is stated rather than dressed up: the HTTP/1 codec
  and the JSON string encoder (the net path's plan, S3 and S5) are the two
  loops it was written for, and ADR 0025 admitted `cache` the same way. Not
  published (ADR 0186 §D10, which amends ADR 0159): a program searches bytes through the domain that
  parses them. Written from the exact lane tests (the non-borrowing mask), not
  from the classic `haszero` macro, whose false hits above a true match are the
  fault the suite's pair test exists for; and it keeps only the searches that
  beat the standard library's — a one-byte search lost to `bytes.IndexByte` on
  the M1 at every length but one and was dropped, its benchmark kept to re-ask
  on x86-64, where it was not measured.
- `jsonenc` admitted on rule 1: stdlib-only, and its signatures name a slice, a string and a number — no logger, no token, no codec. It has four consumers on the day it lands (the logger's JSON encoder, the OTLP emitters, `redact` and the token formats), which replace four string escapers that disagreed on `\b`, `\f`, invalid UTF-8 and U+2028/U+2029. Published through the codec domain rather than on its own (ADR 0186 §D10): it is JSON, and `codec/json` is where a program looks for JSON. It searches with `swar` and holds the one line of `unsafe` the byte search needs. NOT migrated, on purpose: `consensus/wire`, `session/encode.go`, the Prometheus exporter and the logger's text encoder — none is JSON, and a changed byte in consensus forks the nodes.
- `seglog` admitted on rule 1, and as an EXTRACTION before a second copy
  (ADR 0230): its signatures name a directory, an index, a kind byte and a
  payload — no term, no vote, no stream, no queue — and its mechanism is the
  consensus file log's, whose every rule an incident paid for. `data/stream`'s
  file engine (#821) is its first caller; the consensus log keeps its own copy
  until it is migrated, gated on the two fuzzers agreeing. It is the one `fs/`
  member that writes, and it decides nothing: damage is refused unchanged and
  what it means is the caller's.
- `group` and `heap` admitted on **rule 1 alone**, which is the only
  admission criterion there has ever been: stdlib-only AND generic. Each is
  domain-neutral down to its signatures — `Go`/`Wait`/`Collect` with no `Job` or
  `Worker`; a `T` and a `func(a, b T) int` with no `Priority`. **No consumer
  count was required.** The "≥2 concrete consumers" line sometimes attributed
  to ADR 0010 is not a kernel rule: it sits in that ADR's *Deferred* section and
  concerns exactly one primitive (`worker`). The governing precedent is the row
  above it — **ADR 0025 admitted `cache` with ZERO consumers** — and ADR 0010's
  "three copies already existed" was a *consolidation* argument, not a gate.
  Both have consumers now: `group` in `service/app/health` (through `Collect`) and
  `service/data/queue` (through `NewJoined`, which was added for it), `heap` in
  `service/data/queue`'s lease expiry and `service/app/statemachine`'s agenda.
- `topic` — a typed in-process broadcast admitted on the same rule — was
  **deleted** (2026-10). Its only candidate consumer, `service/app/events`, read it
  and refused it in writing (ADR 0053 §D9: a per-subscriber buffer makes
  `Publish` return before the listeners have run and puts `Halt` on the wrong
  side of a channel), and nothing else ever imported it. Admission needs no
  consumer count; keeping a primitive nobody can use still costs a package, a
  `CLAUDE.md`, a `BENCH.md`, linter exemptions and a reader's attention. If a
  fan-out to independent readers is ever needed, the history holds it.
