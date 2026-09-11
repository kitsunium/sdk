<!-- generated from internal/service/proc/sdnotify/sdnotify_bench_test.go — run `cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem -benchtime=1s ./proc/sdnotify/` to refresh -->
# Benchmarks — `internal/service/proc/sdnotify`

**"What am I paying for when a service calls `Ready()` — or `Watchdog()` on a
timer?"**

**0.7 % formatting, 99.3 % socket.** A watchdog ping is a connect/write/close
round trip against an AF_UNIX datagram socket, and nothing this package could do
to its own string handling would show up next to that.

## 1. The whole notifier, decomposed

| | ns/op | share |
|---|---:|---:|
| `EncodePayload_Ready` — build `"READY=1\n"` + the injection check | **168.8** | **0.7 %** |
| `Notify_Ready` — the same, plus `DialUnix` + `Write` + `Close` | **23 680** | 100 % |

Two consequences, both actionable:

**A watchdog on a timer is affordable but not free.** At `WATCHDOG_USEC=30s` the
conventional ping interval is half that — one 24 µs call every 15 s, which is
nothing. At a 100 ms interval it is 24 µs per 100 ms, or 0.024 % of a core, which
is still nothing. There is no interval a supervisor would plausibly configure at
which this package becomes a cost.

**An UNSUPERVISED binary pays 83.7 ns per call and zero allocations.** That is
one `os.LookupEnv` and a nil return — the libsystemd no-op contract. Shipping
`Ready()` / `Status()` / `Watchdog()` unconditionally in a binary that will
sometimes run outside systemd costs nothing measurable, which is the point of the
contract and is now a number.

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Notify_Unset` — `$NOTIFY_SOCKET` unset | 83.68 | 0 | 0 |
| `Notify_Ready` | 23 680 | 688 | 10 |
| `Notify_MainPID` | 24 390 | 712 | 12 |
| `Notify_LongStatus` — 4 KiB `STATUS=` | 34 240 | 10 416 | 12 |

`Notify` opens a fresh connection per call, which is libsystemd's behaviour and
what makes the 24 µs. Persisting the socket would be a different contract, not an
optimisation, and it is not proposed here.

## 2. What the profile named, what was changed, and what was refused

`encodePayload` at six fields was 999 ns / 4 allocs before this change. A CPU
profile named two things and only two:

```
780ms 19.90% 19.90%   780ms 19.90%  indexbytebody
300ms  7.65% 27.55%  1340ms 34.18%  strings.IndexAny        ← the name check
230ms  5.87% 52.81%  1350ms 34.44%  strings.IndexRune
250ms  6.38% 40.82%   950ms 24.23%  runtime.growslice       ← the builder regrowing
```

### Changed: `ContainsAny` → two `Contains` (−24 %, zero behaviour change)

`strings.ContainsAny(name, "\n=")` searches for *runes*. For a name of 8 bytes or
fewer — which every sd_notify field name is — Go's `IndexAny` cannot use its
ASCII-set fast path and falls back to `for i, c := range s { IndexRune(chars, c) }`,
i.e. one `IndexRune` call per byte of the name. That is the 34 % above.

Both delimiters are ASCII, and a UTF-8 continuation byte is never `0x0A` or
`0x3D`, so a byte search and a rune search agree on **every** input, valid UTF-8
or not. Replaced with `strings.Contains(name, "\n") || strings.Contains(name, "=")`
— two `IndexByte` scans, identical semantics, identical allocations.

| workload | before | after | delta |
|---|---:|---:|---:|
| `EncodePayload_Ready` | 219.4 ns | **168.8 ns** | **−23 %** |
| `EncodePayload_Status` | 283.4 ns | **220.8 ns** | −22 % |
| `EncodePayload_SixFields` | 999.1 ns | **726.2 ns** | −27 % |
| `EncodePayload_Rejected` | 484.2 ns | **439.2 ns** | −9 % |
| `EncodePayload_LongStatus` | 1 725 ns | **1 703 ns** | −1 % |

Every row improves and none regresses; allocations are unchanged everywhere.
**Its effect on `Ready()` end to end is −0.2 %**, and this report will not pretend
otherwise. It was kept because it is free, not because it matters.

### Refused: pre-sizing the builder (would make `Ready()` 29 % SLOWER)

The line above `var b strings.Builder` claimed *"pre-size the builder roughly to
avoid repeated growth on small maps"*, and the builder was never pre-sized. The
profile's `growslice` at 24 % looked like the promised change waiting to happen,
so it was implemented — an exact-size pass over the map, then `b.Grow(size)` —
and measured.

| workload | baseline | with `b.Grow` | verdict |
|---|---:|---:|---|
| `EncodePayload_Ready` | 219.4 ns / 1 alloc | **282.7 ns** / 1 alloc | **+29 % — REGRESSION** |
| `EncodePayload_Status` | 283.4 ns / 2 allocs | 292.2 ns / 1 alloc | +3 % |
| `EncodePayload_Rejected` | 484.2 ns / 2 allocs | 630.5 ns / 3 allocs | **+30 % — REGRESSION** |
| `EncodePayload_SixFields` | 999.1 ns / 4 allocs | 837.1 ns / 1 alloc | −16 % |
| `EncodePayload_LongStatus` | 1 725 ns / 2 allocs | 1 689 ns / 1 alloc | −2 % |

The arithmetic cross-checks: an extra `range` over a map costs ~60 ns of iterator
setup (Go randomises the start, and `internal/chacha8rand.block` appears in the
profile for exactly that). `Ready` fits inside the builder's first growth step, so
it pays the 60 ns and saves no allocation — pure loss. `Status` overflows that
step, so the saved 48-byte allocation (~50 ns) nearly cancels it: +9 ns. Both rows
are explained by the same constant, which is why they are believed.

**Refused, and reverted.** It regresses the single most-called function in the
package — every shorthand (`Ready`, `Reloading`, `Stopping`, `Status`, `Watchdog`,
`MainPID`) builds a **one-entry** map, so the six-field case it improves cannot be
produced by this package's own API at all. The misleading comment was replaced
with the measured reason for not pre-sizing.

## 3. The injection check is the expensive part, and that is correct

`TestNotifyRejectsFieldInjection` exists because a `\n` in a value or a `=` in a
name would forge extra protocol fields. After the change above, the scan is still
the largest single component of `encodePayload` — and it should be.

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `EncodePayload_Ready` (1 field) | 168.8 | 8 | 1 |
| `EncodePayload_Status` (1 free-text field) | 220.8 | 56 | 2 |
| `EncodePayload_SixFields` | 726.2 | 247 | 4 |
| `EncodePayload_LongStatus` (4 KiB) | 1 703 | 4 872 | 2 |
| `EncodePayload_Rejected` (a `\n` in `STATUS`) | 439.2 | 208 | 2 |

Two properties worth stating because a security check should be measured, not
assumed:

- **The scan is linear in the value, with a small constant.** A 4 KiB `STATUS`
  costs 1.7 µs to validate and encode — 5 % of the 34 µs it takes to send. There
  is no input size at which validation, rather than the socket, becomes the cost.
- **A REJECTED field costs 2.6× an accepted one** (439 ns vs 169 ns), and the
  difference is building the typed `InvalidNotification`, not the scan. An
  attacker who can choose the field values can therefore make the call 2.6×
  more expensive and 0 % more successful. Against a 24 µs socket round trip the
  ratio is not reachable in practice.

## 4. The rest: environment queries and the listener side

| workload | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `ResolveAddr_Pathname` | 2.827 | 0 | 0 |
| `ResolveAddr_Abstract` — `@` → NUL, one concat | 58.54 | 48 | 1 |
| `WatchdogInterval_Unset` | 52.00 | 0 | 0 |
| `WatchdogInterval_Set` | 87.24 | 0 | 0 |
| `ParsePayload_Ready` | 315.3 | 336 | 2 |
| `ParsePayload_SixFields` | 636.7 | 336 | 2 |

`ResolveAddr_Abstract` allocates once per `Notify` because the abstract-namespace
form must build `"\x00" + raw[1:]`. 56 ns and 48 B on a 24 µs call: **0.2 %**.
Caching it would mean caching an environment variable, which the package
deliberately re-reads so a supervisor can change it.

`parsePayload` (the `Listen`/`Recv` side) allocates the `State` map — 336 B — and
that is by contract: the doc promises `State` is always non-nil so callers can
index without a guard. Six fields cost 2× one field for 6× the work, because the
map allocation is the fixed part.

**Verdict for this package: nothing further to optimise.** The one change made
was free and is worth 0.2 % of the call it is in; the one change refused would
have cost 29 % on the most common call. The package's real cost is a socket, and
that belongs to the kernel.

## Reproducibility envelope

> Every figure is the **median of 3 full runs**, each a separate process. Spread
> was under 4 % on all but three rows (`LongStatus` 10 %, `ParsePayload_*`
> 9–13 %); min and max are in the results block. The before/after tables in §2
> are single 1 s runs of the same benchmark on the same idle machine minutes
> apart — they are directional and each delta is far larger than the run-to-run
> spread of its row. Load average was 0.35–2.61 across the session.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor (8 cores visible) |
| RAM | 15 GiB |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | jaimerias-que-tu-te-connect |
| Git commit | 1da8e4c |
| Generated (UTC) | 2026-09-10 |
| Bench wall-clock | `-benchtime=1s`, median of 3 runs |

## Results

```
goos: linux
goarch: amd64
pkg: github.com/kitsunium/sdk/internal/service/proc/sdnotify
cpu: AMD EPYC 7351P 16-Core Processor

benchmark                        median ns/op   spread    B/op  allocs/op
EncodePayload_Ready-8                   168.8     0.7%       8      1
EncodePayload_Status-8                  220.8     1.9%      56      2
EncodePayload_SixFields-8               726.2     3.2%     247      4
EncodePayload_LongStatus-8              1 703    10.0%    4872      2
EncodePayload_Rejected-8                439.2     1.9%     208      2
ParsePayload_Ready-8                    315.3     9.3%     336      2
ParsePayload_SixFields-8                636.7    12.5%     336      2
ResolveAddr_Pathname-8                  2.827     2.1%       0      0
ResolveAddr_Abstract-8                  58.54     3.3%      48      1
WatchdogInterval_Set-8                  87.24     3.7%       0      0
WatchdogInterval_Unset-8                52.00     5.2%       0      0
Notify_Unset-8                          83.68     2.8%       0      0
Notify_Ready-8                         23 680     3.4%     688     10
Notify_MainPID-8                       24 390     2.5%     712     12
Notify_LongStatus-8                    34 240     1.7%   10416     12
```
