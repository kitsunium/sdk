# ADR 0215 — a cgroup's BPF programs are built in Go, in a quarantined vendor module

- **Status**: Accepted
- **Date**: 2026-11-08
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) —
  a vendor module more, `third-party/ebpf`; [ADR 0016](0016-sdk-process-supervision-domain.md)
  — the proc domain gains a contract, `core/proc/bpf`, an engine,
  `service/proc/bpf`, and a facade, `pkg/v1/proc/bpf`, whose backend is
  outside the standard library.
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a
  verdict is never the zero), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md)
  (the ports are frozen), [ADR 0136](0136-every-module-is-scanned-for-the-vulnerabilities-it-reaches.md)
  (the scan), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (the
  registry is `kernel/plugin`'s), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md)
  (the codes in the core, at the engine's path); kitsunium/sdk#731 (S6), #727 (S1,
  `GroupTree.Path`), #734 (S0g) and #728 (S2), its consumers;
  supervizio/agent#326, #330, #331.

## Context

systemd confines a unit with BPF programs attached to its cgroup:
`DeviceAllow=` and `DevicePolicy=` are a `cgroup_device` program,
`IPAddressAllow=`, `IPAddressDeny=` and `IPAccounting=` a pair of
`cgroup_skb` programs over longest-prefix-match maps, `SocketBindAllow=` and
`SocketBindDeny=` a pair of `cgroup_sock_addr` programs. The agent that
replaces systemd for its units (supervizio/agent#326) needs the same, and its
roadmap names them: #330 (devices, IP accounting and filtering) and #331 (the
sandbox's BPF parts). The SDK had none of it.

The standard library cannot load a BPF program: `bpf(2)` is a syscall with
thirty-odd commands, a verifier whose log is the only explanation of a
refusal, maps, BTF and kernel-version probes. The user decided (D3) to take
`cilium/ebpf` for it, quarantined under `third-party/` as ADR 0157 lays a
vendor out, with the SDK's stdlib tree free of it.

## Decision

### 1. The contract and the engine in the standard library tree, the backend in a vendor module

`internal/core/proc/bpf` is the contract: the three policies as plain values
(`DevicePolicyValue`, `SKBPolicyValue`, `BindPolicyValue` and their rules,
`CountersValue`), their validation (`Validate`, `CheckCgroup`), the ports —
`Backend`, which a backend implements, and `Program`, `SKBProgram`, which an
attachment satisfies — and the codes `0.3.107.*`.
`internal/service/proc/bpf` is the engine at the same path (ADR 0160 §1, the
range allocated to it): a one-slot registry (`kernel/plugin.Registry`, ADR
0159), `Register`, `Probe`, `AttachDevice`, `AttachSKB` and `AttachBind`,
each validating the policy, then the path, then looking for the backend.
`pkg/v1/proc/bpf` re-exports both, every symbol written by kit from
`design/proc/bpf.yaml`.

The backend is `third-party/ebpf/cgroup`, in the module `third-party/ebpf`
that requires `internal`, `cilium/ebpf` and `golang.org/x/sys`. Importing it —
blank is enough — registers its `Backend` with the engine at package
initialisation (`var Backend = svcbpf.Register(…)`, no `init`). A program
that does not import it gets `NOT_LINKED` from `Probe` and from every
`Attach`, never a silent no-op, and links nothing of cilium/ebpf:
`pkg/v1/proc`'s `go.mod` does not require it, so the go command could not
even build a dependency on it.

The agent calls the facade, and S0 (`proc/service`) is to call the engine,
each with a group's directory — a `GroupTree.Path()` (#727), a `CgroupUnit`'s
`Path()` (#734). The engine does not call it yet: no `Config` field carries a
policy, and the wiring is future work (ADR 0214's amendment on the BPF rows).
Neither imports the vendor's package; the agent's one blank import in its
`main` links it.

### 2. Why cilium/ebpf

| Option | Verdict |
|---|---|
| `bpf(2)` by hand, in the stdlib tree | refused: map and program creation, attachment, the verifier's log, BTF and the feature probes are the library, rewritten and maintained by the SDK |
| libbpf through cgo (`libbpfgo`) | refused: cgo, a C toolchain and a shared library in every consumer's build, and `CGO_ENABLED=0` lanes |
| `cilium/ebpf` | taken: pure Go, MIT, the asm package needs no clang, the loader used by Cilium, Tetragon and runc's successors |

Version **v0.22.0**, published 2026-06-26, the latest; `go mod download` and
`go mod verify` pass. `golang.org/x/sys` is required at **v0.48.0**, the
workspace's version, not cilium's v0.43.0: v0.43.0 carries GO-2026-5024
(`golang.org/x/sys/windows`, fixed in v0.44.0), which govulncheck reports
as a required module.

Licences: cilium/ebpf MIT; golang.org/x/sys BSD-3-Clause — the two modules
that provide packages to the build. The rest of the module graph, required
by cilium/ebpf's tests and tools and linked by nothing here, is MIT
(go-quicktest/qt, jsimonetti/rtnetlink, josharian/native, mdlayher/netlink,
mdlayher/socket, kr/pretty, kr/text) or BSD-3-Clause (google/go-cmp,
rogpeppe/go-internal, golang.org/x/net, golang.org/x/sync), among those
`go mod verify` downloads.

### 3. The cost, measured

| Measure | Value |
|---|---|
| `go list -deps ./cgroup`, linux/amd64 and linux/arm64 | 155 packages: 132 standard, 16 of `cilium/ebpf`, 1 of `golang.org/x/sys` (`unix`), 6 of the SDK |
| `go list -deps ./cgroup`, darwin/arm64 | 138 packages: 123 standard, 8 of `cilium/ebpf` (`asm` and what it needs), 7 of the SDK |
| `go list -m all`, the module | 38 modules, of which 2 provide a package to the build (`cilium/ebpf`, `golang.org/x/sys`) |
| `go list -deps ./bpf`, `pkg/v1/proc` | 72 packages, 0 of `cilium/ebpf`; `go list -m all` there lists no `cilium/ebpf` |
| `govulncheck ./...` (v1.8.0, database of 2026-11-05) | `No vulnerabilities found.`, on linux and on windows |

None of the cilium packages linked has an `init` that touches the process
(`features`' and `sys`' fill tables). `rlimit` has one — it lowers
`RLIMIT_MEMLOCK` to zero and calls `bpf(2)` in every program that links it —
and is not imported (§6).

### 4. The programs are built with `asm`, never from an object file

#731 asks for `cilium/ebpf/asm` only, no ELF and no `bpf2go` output committed.
Every program is a function of its policy, a few instructions:

- a device policy is its rules in order, compiled in a straight line — one
  comparison per field that is not a wildcard, a jump to the next rule on a
  mismatch, then `r0 = verdict; exit` — and its default last. An `Allow` rule
  matches a request whose every access it covers, a `Deny` rule one whose
  access it shares, so a read-write open is not allowed by a read rule and is
  denied by a write-deny rule. A number past `int32` is compared through a
  register: an immediate is sign-extended;
- a bind policy is the same over the port (`user_port`, swapped to host
  order) and the protocol, one program per family on its bind hook;
- an IP policy is a pair of `cgroup_skb` programs, ingress and egress: the
  peer's address — a received packet's source, a sent one's destination — is
  read with `skb_load_bytes` into a key on the stack and looked up in an LPM
  trie per family; the verdict lands in R8; an array of two slots counts
  every packet and its bytes, and the dropped ones apart, by atomic adds.

A rule that compares nothing ends its program: what follows is unreachable,
and the verifier refuses an unreachable instruction. Every exit returns a
constant 0 or 1, the range the verifier holds a cgroup program to.

So the CI needs no clang and no LLVM, and a policy is a value, not a file to
regenerate. The cost is that the programs never meet a kernel's verifier on
a machine without the privilege; §8 says what stands in for it.

### 5. Attached beside the others, until `Close`

Each program is attached with `BPF_PROG_ATTACH` and `BPF_F_ALLOW_MULTI`, and
detached by its own descriptor: it runs beside the programs already on the
group, an operation passes only when every program of the group and of its
ancestors lets it through, and it replaces none. `link.AttachCgroup` is not
used: it tries `bpf_link`, whose attachment goes with the last descriptor —
a supervisor that crashes would release its services from their device and
bind policies — and falls back to `BPF_F_ALLOW_OVERRIDE`, which replaces what
is there. The attachment is the kernel's: it outlives the process that made
it, until `Close` or the group's removal.

The group is opened first, and refused (`ATTACH_FAILED`) when it is not on
cgroup2fs or has no `cgroup.type` — the hierarchy's root, the machine's or a
cgroup namespace's —, where a policy would bind everything.

### 6. `RLIMIT_MEMLOCK` before 5.11, lazily

A kernel before 5.11 charges BPF memory to `RLIMIT_MEMLOCK`. Before the first
load, on such a kernel (uname's release, or one it cannot read), the limit is
raised to infinity, once per process; without `CAP_SYS_RESOURCE` every load is
`LOAD_REFUSED`. From 5.11 nothing is touched. This is cilium's
`rlimit.RemoveMemlock` decision without its `init`.

### 7. Every refusal is typed

| Where | Answer |
|---|---|
| a verdict that is the zero, a rule out of range, too many rules, a relative or unclean path | `POLICY_INVALID` (`0.3.107.1`), before anything else, the rule and the field named |
| no backend linked | `NOT_LINKED` (`0.3.107.2`) |
| off Linux on amd64 and arm64 | `core/proc`'s `UNSUPPORTED_PLATFORM`, re-exported by the facade |
| no `CAP_BPF`, the memlock limit, a kernel without the type, the verifier | `LOAD_REFUSED` (`0.3.107.3`), the kernel's error and the verifier's log in the chain |
| a directory that is no group, the hierarchy's root, `EPERM` on attach | `ATTACH_FAILED` (`0.3.107.4`) |
| a detach or a counter read that failed, `Counters` after `Close` | `PROGRAM_FAILED` (`0.3.107.5`) |
| a nil or second backend at import | a panic, `BACKEND_REFUSED` (`0.3.107.6`) |

The build constraint is `linux && (amd64 || arm64)` for the loader; the
program builders are pure Go and compile, and are tested, on every cell.

### 8. Tests, and what stands in for the verifier

- An interpreter of the instructions the builders emit
  (`vm_internal_test.go`), strict where the verifier is: a jump to a missing
  label, an unreachable or fall-off instruction, an exit without a constant 0
  or 1, a read of an unwritten stack byte, a register read after a call
  clobbered it, a helper argument off the stack. Hand-written and seeded
  random policies run through it against a reference written from the
  documentation, on every platform and without privilege; seven mutations of
  the builders — the access test, the immediate's sign, the verdict, the
  ingress offset, the port's byte order, the range's last port, a null check
  — were each caught.
- The memlock decision is simulated: 5.10, 6.12, an unreadable release,
  sixteen concurrent first loads raising once, a refused raise.
- A live suite (`live_linux_external_test.go`), root and a cgroup to create a
  group under: `/dev/null` refused then allowed again, two device programs at
  once (`BPF_F_ALLOW_MULTI`), datagrams counted and one dropped with `EPERM`,
  a TCP bind refused while UDP and port 0 are not, the hierarchy's root
  refused. It spawns its own binary into a group it creates with
  `CLONE_INTO_CGROUP`, never attaches elsewhere, and skips, after checking
  the refusal is `LOAD_REFUSED`, without the privilege. `e2e-cross.yml`'s
  `linux-init` lane runs it as root in a delegated unit.
- `garble -tiny` (`garble_external_test.go`, the `garble-tiny` job of
  `bazel-ci.yml`): a program that imports the package blank, built with
  garble v0.18.0 `-tiny`, still registers the backend and answers with typed
  codes.

## Consequences

- A consumer opts in with one import, and pays for cilium/ebpf only then.
- The agent's unit directives map onto three values; their YAML stays the
  agent's.
- A program attached by a supervisor that is killed stays on the group until
  the group is removed; a supervisor that restarts and attaches again adds a
  second program beside the first, which is harmless for a deny and counts
  twice for the IP counters — it must `Close` what it attached, or remove the
  group.

## Never run here

The VM this was written on has `unprivileged_bpf_disabled=2`, no root and no
container: no program met a kernel verifier, none was attached, and arm64
never ran at all. The live suite, the attachments and `BPF_F_ALLOW_MULTI` are
the `linux-init` lane's to prove; the garble smoke and every unprivileged
test ran here.

## Deferred

- `bpf_link` as an opt-in lifetime, for a caller that wants its programs to
  go with it.
- Per-CPU counters, and counters per prefix (systemd counts per unit only).
- A row in proc's capability matrix: `bpf.Probe` answers the question at
  wiring time, and a probe in `core/proc` would import its own child.
- arm64 in a live lane: no lane boots an arm64 kernel as root.

## References

- `cilium/ebpf` v0.22.0, `asm`, `link.RawAttachProgram`, `rlimit` —
  https://github.com/cilium/ebpf
- include/uapi/linux/bpf.h: `struct bpf_cgroup_dev_ctx`, `struct __sk_buff`,
  `struct bpf_sock_addr`, `BPF_F_ALLOW_MULTI`
- systemd.resource-control(5): `DeviceAllow=`, `IPAddressAllow=`,
  `IPAccounting=`, `SocketBindAllow=`
- Linux commit 97306be45fbe (5.11): memory-cgroup accounting of BPF memory
