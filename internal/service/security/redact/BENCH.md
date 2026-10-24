<!-- generated from internal/service/security/redact/json_bench_test.go — run `cd internal && GOWORK=off go test -run='^$' -bench=. -benchmem -count=8 -benchtime=100ms ./service/security/redact/` to refresh; not `make bench`, which regenerates the codec's report only -->
# Benchmarks — `internal/service/security/redact`

One benchmark, `BenchmarkJSON`: `Redactor.JSON` on a document whose cost is its
strings, before and after the whole-string path moved to `kernel/jsonenc`
(`appendQuoted`). The cut path (a string that does not fit its bound) still
writes rune by rune and was not changed.

> Taken on a busy machine (load average 17 to 69 on 10 cores): the minimum of 8
> alternated rounds, the origin/main binary against this branch's. Apple M1 Pro,
> go1.27.3 darwin/arm64, 2026-10-24, base `6f89b0852`.

| Document | before ns/op | after ns/op | delta | allocs/op |
|---|---|---|---|---|
| short (four short members) | 1233 | 969 | -21.4 % | 14, unchanged |
| unicode (accented, CJK) | 1945 | 1627 | -16.3 % | 18, unchanged |
| long (three 176-byte strings) | 10487 | 5815 | -44.6 % | 16, unchanged |

The per-run noise on this machine was larger than the short-document delta;
the long-document delta is the one to trust. See `internal/kernel/jsonenc/BENCH.md`
for the escaper itself.
