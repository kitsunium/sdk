# bsdabi

The records the BSD kernels and darwin hand the proc family, decoded from
their bytes — a process's start time and users from `kinfo_proc`, a
datagram's sender from FreeBSD's `sockcred2`.

```go
func ProcRequest(goos, goarch string, pid int) (mib []int32, size int, ok bool)
func ProcRecord(goos, goarch string, pid int, buf []byte) (start int64, ruid, euid int, ok bool)
func Sockcred2(data []byte) (pid, uid, gid int, ok bool)
```

No system call and no build constraint: every layout is tested on any machine.
`proc/exec` asks the kernel and decodes with it off Linux (#729);
`proc/systemd/notify` reads a FreeBSD datagram's sender with it (#730).

Internal to `internal/service/proc`. See `CLAUDE.md`.
