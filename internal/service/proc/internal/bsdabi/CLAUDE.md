<!-- updated: 2026-11-08T00:00:00Z -->
# internal/service/proc/internal/bsdabi/

## Purpose

The **records the BSD kernels and darwin hand the proc family, decoded from
their bytes**: a process's `kinfo_proc` (`kinfo_proc2` on NetBSD) — its start
time and its real and effective users, which `proc/exec`'s adoption reads off
Linux (#729) —, and FreeBSD's `struct sockcred2` — a datagram's sender, which
`proc/systemd/notify`'s listener reads there (#730). ADR 0219 amended
2026-11-08.

No system call and no build constraint: the caller passes `runtime.GOOS` and
`runtime.GOARCH` and does the `sysctl(3)` itself, so every layout is tested on
any machine, Linux included. A record is decoded only once it proves to be the
one asked for — its size, the size it states where it states one (FreeBSD's
`ki_structsize`), the pid it names —; anything else is `ok == false`, never a
guess.

Code range: **none** — it decodes bytes and reports `ok`.

Importers: `proc/exec` (`attach_bsd.go`), `proc/systemd/notify`
(`listen_freebsd.go`).

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `proc.go` | `ProcRequest` (the sysctl name and buffer size), `ProcRecord` (start, ruid, euid); one `procLayout` per kernel and ABI, every offset a named constant citing its header |
| `sockcred.go` | `Sockcred2`: `struct sockcred2` (sys/socket.h) — version 0, the sender's PID, real uid and real gid, the groups it counts present, at most `CMGROUP_MAX` (16) |
| `sockcred_external_test.go` | the structure transcribed member by member, laid out by the same C rules; 0, 1 and 16 groups; the refusals |
| `proc_external_test.go` | `cStruct` and `cUnion`, the C rules, over member-by-member transcriptions of each header: the record built at the transcription's offsets must decode; the refusals |

## The layouts, and how they were checked

| Kernel | Name | Record | Offsets (pid, euid, ruid, sec, usec) |
|---|---|---|---|
| FreeBSD amd64, arm64, riscv64 | `{1, 14, 1, pid}` | `kinfo_proc`, 1088 (`KINFO_PROC_SIZE`) | 72, 168, 172, 336 (8), 344 (8) |
| FreeBSD 386 | same | 768 | 40, 136, 140, 280 (4), 284 (4) |
| FreeBSD arm | same | 816 | 40, 136, 140, 280 (8), 288 (4) |
| DragonFly amd64 | `{1, 14, 1, pid}` | `kinfo_proc`, prefix of ≥ 224 in a 4096 buffer | 220, 132, 204, 96 (8), 104 (8) |
| darwin amd64, arm64 | `{1, 14, 1, pid}` | `kinfo_proc`, 648 | 40, 420, 392, 0 (8), 8 (4) |
| OpenBSD, every arch | `{1, 66, 1, pid, 424, 1}` | `kinfo_proc` prefix, 424 | 108, 128, 132, 408 (8), 416 (4) |
| NetBSD, every arch | `{1, 47, 1, pid, 464, 1}` | `kinfo_proc2` prefix, 464 | 116, 136, 140, 456 (4), 460 (4) |

Each offset was computed twice before it was written: by gcc from the header
(sys/user.h, sys/kinfo.h, xnu's bsd/sys/sysctl.h and proc.h, sys/sysctl.h),
with the ABI's types and alignments, the total checked against the header's
own size (`KINFO_PROC_SIZE` 1088, 768, 816; darwin 648); and, where they
exist, from the cgo `-godefs` structs of `golang.org/x/sys/unix` (darwin) and
`github.com/shirou/gopsutil` (FreeBSD). OpenBSD and NetBSD are asked for the
prefix read: their kernels copy `min(asked, sizeof)` (`sysctl_doproc`,
`sysctl_doeproc`), and that prefix is laid out alike on every architecture.

`struct sockcred2` is seven 32-bit fields then the groups: `sc_version` 0,
`sc_pid` 4, `sc_uid` 8, `sc_euid` 12, `sc_gid` 16, `sc_egid` 20,
`sc_ngroups` 24, `sc_groups` 28; `SOCKCRED2SIZE(n)` = 28 + 4·n, the kernel
writing `sc_version = 0` and at most `CMGROUP_MAX` groups (`uipc_usrreq.c`,
`unp_addsockcred`). The REAL ids are returned, as Linux's `SCM_CREDENTIALS`
carries by default.

## Do NOT

- Read a field without a layout that proves the record first.
- Add a system call here: the caller owns it, the tests own every layout.
- Compare the start time across a long interval: FreeBSD and OpenBSD add the
  boot time on every read, and a stepped clock moves it (ADR 0219, amended).
