# rlim

The kernel's resource-limit struct, built from a soft/hard pair — the one
constructor the proc family's engines share.

```go
func Make(soft, hard uint64) syscall.Rlimit
```

`proc/exec` (the trampoline's pre-exec `setrlimit`) and `proc/rlimit`
(`setrlimit`, and `prlimit64` on Linux) both call it. FreeBSD and DragonFly
declare `Rlimit.Cur`/`Max` as `int64`, every other Unix as `uint64`; `Make` is
split by build tag so no caller names the field type, and `^uint64(0)` — no
limit — lands as `RLIM_INFINITY` on both.

Internal to `internal/service/proc`. See `CLAUDE.md`.
