# Allocation hot paths — the ten per-record rows of #813

Issue [#813](https://github.com/kitsunium/sdk/issues/813) ranked, by
allocations per operation, the paths a service runs per record or per request
(start-up rows excluded), at `ee1f48ba2`. This page measures the same ten rows
again on one base and names the cause of each. **It changes no code but row
8's**: row 8 is the kernel's own, and the kernel lot fixes it (ADR 0216
§Decision 8). Every other row lives in a package another change owns (the
data domains, the observe domain, the app domain); each fix is that
package's own change, with the row below as its "before" and an `allocs:`
budget in `design/sdk.yaml` as its guard (ADR 0165) — row 8's is there.
Decided in ADR 0216 §Deferred.

## How it was measured

- Base: `origin/main` at `42143bf47` (after the data lot L6 and the codec and
  observability lot L5), on the kernel lot's branch, which touches none of
  these packages.
- Box: AMD EPYC 7351P, 8 vCPU, Linux 6.12, go1.27.3, `GOMAXPROCS=4`.
- `go test -run '^$' -bench <row> -benchmem -count 3` with a fixed
  `-benchtime` per row (200x for the file and OTLP rows, 300x for vfs, 2000x
  for docstore and health, 20000x for the rest). `allocs/op` were identical
  across the three runs of every row; `ns/op` is the median and an order of
  magnitude only.

## The ten rows

| # | Row | At `ee1f48ba2` (#813) | At `42143bf47` | Cause |
|---|---|---|---|---|
| 1 | `data/queue` `ReceiveBatch/file/batch1` | 1 107 allocs | **362 allocs**, 32 218 B, ~358 µs | The two `fs.ReadDir` per `Receive` #813 named were replaced by a listing reused while the directory is quiet (L6, #1043); what is left is the listing when it is not, the claim's rename and the record's decode. `batch16`: 1 506 allocs |
| 2 | `data/queue` `Publish/file/64` | 41 allocs | **50 allocs**, ~2 700 B, ~3.6 ms | `randomHex` entropy and the message name's formatting (`file.go`), on top of row 3's `WriteAtomic`, whose allocations rose (row 3) |
| 3 | `data/vfs` `WriteAtomic/os/256` | 24 allocs (vs `WriteFile` 8) | **32 allocs**, ~1 830 B, ~3.3 ms (`WriteFile/os/256`: 8) | The temporary name — `rand.Read` + `hex.EncodeToString` + `path.Join` (`os_publish.go`) — and two descriptors; +8 since the directory fsync goes through a `commit.Group` per directory (ADR 0207: "one publisher unchanged in time, +8 allocations") |
| 4 | `data/docstore` `Put/docs=100` | 27 allocs | **27 allocs** (28 at 10 000 and 100 000 documents), ~1 780 B | `json.Marshal` of the entry record per write (`persist.go`), the hex entry name, then row 3's `WriteAtomic` on the disk engine |
| 5 | `data/docstore` `Get` / `Lookup` | 11 / 15 allocs | **11 / 15 allocs**, 336 / 424 B | The decode into a fresh `T` per read — "a copy of the stored document" is the contract; a `GetInto(key, dst)` sibling for callers that own the value is the shape #813 proposes |
| 6 | `observe/internal/otlp` `BuildAndMarshal_1000` | 9 014 allocs | **9 013 allocs**, ~864 KB | ~9 per point: the tree built per export and a `[]KeyValue` per point; one arena per export sized from the point count, sub-sliced per point, is the BENCH commentary's own shape |
| 7 | `observe/logger` `MW_EncWrite` | 6 allocs | **5 allocs**, 1 696 B | `encwrite.go`: the frame `make([]byte, lenPrefixBytes+len(box))` after a `Seal` that returns a fresh box — no append form of `Seal` (`SealTo(dst, …)`) exists in `design/crypto.yaml` |
| 8 | `kernel/concur/singleflight` `Do_Uncontended` | 6 allocs, 368 B | **6 allocs**, 384 B — **5 allocs**, 384 B since the kernel lot | The leader's join: `context.WithoutCancel` + `context.WithCancel`, the call record, its done channel and the `go` statement's closure. **Fixed by the kernel lot**: the root `WithoutCancel` built — the leader's values, none of its cancellation — lives in the call record, and `WithCancel` is built over it; one allocation fewer, B/op unchanged (the record grows from 80 to 96 bytes), time within noise (benchstat, n = 15, `singleflight/BENCH.md`). The output is identical: `identity_internal_test.go` compares the context fn sees, its children, and Do's values, errors and panics with the construction before, and passes on both codes. Budget `allocs: 5` on `(*Group[...]).Do`. One context type cancelling itself, #813's sketch, was not taken: it changes `context.Cause` and the children's names. The goroutine stays by design (ADR 0049) |
| 9 | `app/health` `ProbeReadiness_32` | 355 allocs, 26 005 B | **584 allocs**, 40 483 B, ~195 µs | Per check, per probe: a goroutine and its closure from `group.Collect(ctx, group.Unlimited, checks)` (`probe.go`) and two contexts (`entry.go`, `WithCancel(WithoutCancel(parent))`). The rise since the audit is not attributed here. `pool` (ADR 0216) is the primitive a probe would run its checks on |
| 10 | `data/cache` `Set_OneTag` vs `Set_Untagged` | 3 vs 2 allocs | **3 vs 2 allocs**, 330 vs 253 B, ~1.4 µs vs ~0.6 µs | `tagindex.go` `add`: the owned copy of the tags (`make([]string, 0, len(tags))`, kept as the entry's slice so the caller may reuse its own) is the third allocation; a first sight of a tag adds its bucket map besides. The time is the two map writes per tag |

`pkg/v1/net/client` `Get_Parallel` (119 allocs) stays excluded, as in #813:
its BENCH attributes everything under `Client.Get` to `net/http`.
