<!-- updated: 2026-11-08T12:00:00Z -->
# third-party/ebpf/cgroup/

## Purpose

The cgroup BPF programs' backend over cilium/ebpf (ADR 0215, kitsunium/sdk#731
S6), in the `third-party/ebpf` vendor module (ADR 0157). Importing the package
— blank is enough — registers `Backend` with the engine
`internal/service/proc/bpf`; from then on the engine and `pkg/v1/proc/bpf`
load and attach `cgroup_device`,
`cgroup_skb` and `cgroup_sock_addr` programs instead of answering
`NOT_LINKED`. No code of its own: every refusal is a `core/proc/bpf`
sentinel (`0.3.107.*`) or `core/proc`'s `UNSUPPORTED_PLATFORM`.

## Surface (design: `design/third-party/ebpf.yaml`)

`Backend` — the `corebpf.Backend` registered with `svcbpf.Register`. Nothing else is exported.

## The programs, built with `asm`, never from an object file

| Policy | Program(s) | Shape |
|---|---|---|
| `DevicePolicyValue` | one `cgroup_device` | request in R2–R5; per rule, a jump to the next rule per field that is not a wildcard, then `r0 = verdict; exit`; the default last. A number past `int32` is compared through a register (an immediate is sign-extended) |
| `SKBPolicyValue` | `cgroup_skb` on `BPF_CGROUP_INET_INGRESS` and `…_EGRESS` | the peer's address (source in, destination out) read with `skb_load_bytes` into an LPM key on the stack, looked up in `lpm4` or `lpm6`; the verdict in R8; `counters[slot]` gets packets, bytes and, for a drop, dropped packets and bytes, by atomic adds |
| `BindPolicyValue` | `cgroup_sock_addr` on `BPF_CGROUP_INET4_BIND` and `…6_BIND` | the port (`user_port`, network order) swapped to host order in R2, the protocol in R3; the rules of the hook's family in order; the default last |

A rule that compares nothing ends its program: what follows would be
unreachable, and the verifier refuses an unreachable instruction. Every exit
returns a constant 0 or 1, the range the verifier holds a cgroup program to.

## Loading and attaching (`load_linux.go`, `linux && (amd64 || arm64)`)

- `memlocker` (`memlock.go`): before the first load, on a kernel before 5.11
  — `uname`'s release, or an unreadable one —, `RLIMIT_MEMLOCK` is raised to
  infinity once per process; refused, every load is `LOAD_REFUSED`. cilium's
  `rlimit` package is NOT imported: its `init` lowers the limit to zero and
  calls `bpf()` in every program that links it.
- `open`: the group's directory is opened and held; `fstatfs` must say
  cgroup2fs, the directory must have a `cgroup.type` (the machine's root
  has none) and must not be the top of its cgroup2 mount (a cgroup
  namespace's root, whose parent directory lies on another filesystem);
  each refused as `ATTACH_FAILED`, step `hierarchy-root`.
- `handle.add`: `ebpf.NewProgram` (`LOAD_REFUSED`, the verifier's log in the
  chain), then `link.RawAttachProgram` with `BPF_F_ALLOW_MULTI` and nothing
  else (`ATTACH_FAILED`). Not `link.AttachCgroup`: it tries `bpf_link` — gone
  with the process — and falls back to `BPF_F_ALLOW_OVERRIDE`, which replaces.
- `Close`: `link.RawDetachProgram` by each program's descriptor, the last
  first, then the programs, maps and directory are closed; a second `Close`
  is nil. A failure part-way through an `Attach*` undoes what was attached.

Off Linux amd64/arm64 (`load_other.go`) every verb is `UNSUPPORTED_PLATFORM`;
the program builders still compile and are tested everywhere.

## Contents

| File | Role |
|---|---|
| `backend.go` | `Backend`, the registration, the second validation |
| `device.go`, `skb.go`, `bind.go` | the three program builders and the kernel's layouts they read |
| `memlock.go` | `memlocker`, `needsMemlock`, `kernelVersion` |
| `load_linux.go`, `load_other.go` | the loader and the refusal |
| `doc.go`, `gen_api_test.go` | written by kit gen from the design |
| `testdata/smoke/` | the program the `garble -tiny` check builds |

## Tests

- **Every lane, no privilege** — `vm_internal_test.go` is an interpreter of
  the instructions the builders emit, strict where the verifier is (a jump to
  a missing label, an unreachable or a fall-off instruction, an exit without a
  constant 0 or 1, a read of an unwritten stack byte, a register read after a
  call clobbered it, a helper argument off the stack). `device_`, `bind_` and
  `skb_internal_test.go` run hand-written and seeded random policies through
  it against a reference written from the documentation; seven mutations of
  the builders were each caught. `memlock_internal_test.go` simulates 5.10,
  6.12 and an unreadable release; `backend_external_test.go` checks the
  registration and the refusals before any load.
- **Linux, no privilege** — `refusal_linux_external_test.go`: a plain
  directory is `ATTACH_FAILED` before anything loads; `Probe` is
  `LOAD_REFUSED`.
- **Linux, root, a cgroup to create a group under** —
  `live_linux_external_test.go`: a device denied, two programs at once
  (`BPF_F_ALLOW_MULTI`), datagrams counted and one dropped with `EPERM`, a
  bind refused, the hierarchy's root refused. It spawns its own binary into a
  group it creates (`CLONE_INTO_CGROUP`) under `KITSUNIUM_CGROUP_TEST_ROOT` or
  its own group, never attaches elsewhere, and skips without the privilege.
  CI's `linux-init` lane runs it as root in a delegated unit
  (`e2e-cross.yml`).
- **garble -tiny** — `garble_external_test.go` builds `testdata/smoke` with
  `garble -tiny` and runs it; opt-in by `KITSUNIUM_GARBLE`, run by CI's
  `garble-tiny` job (`bazel-ci.yml`, rule 12).

## Do NOT

- Commit an ELF object or a `bpf2go` output: the programs are `asm` (#731).
- Attach with `BPF_F_ALLOW_OVERRIDE` or through `link.AttachCgroup`.
- Import `cilium/ebpf/rlimit`, or any cilium package with an `init` that
  touches the process.
- Attach, in a test, anywhere but to a group the test created.
