<!-- updated: 2026-11-08T12:00:00Z -->
# internal/service/proc/bpf/

## Purpose

The engine of the cgroup BPF programs (ADR 0215, kitsunium/sdk#731 S6): the
one backend a program links, and the verbs that hand it what
`internal/core/proc/bpf` validated. The backend is a vendor module,
`third-party/ebpf/cgroup` over cilium/ebpf, which calls `Register` when it is
imported; a program that does not import it links nothing of cilium/ebpf and
is answered `NOT_LINKED`. Codes: none here — the core's `0.3.107.*`, at this
path (ADR 0160).

## Contents

| File | Surface |
|---|---|
| `doc.go` | the package comment — kit writes it from `design/proc/bpf.yaml` |
| `register.go` | `Register` (panics `BACKEND_REFUSED` at import on a nil, non-comparable or second backend; the same one twice is idempotent), `Probe`, `AttachDevice`, `AttachSKB`, `AttachBind` — the policy, then the path, then the backend |

## Conventions

- **The registry is `kernel/plugin.Registry`** (ADR 0159), one slot,
  `"cgroup"`, behind a pointer a test stands a fresh one in for.
- **`POLICY_INVALID` before `NOT_LINKED`**: a refused policy or path is the
  caller's to fix whether a backend is linked or not.
- **Never a silent no-op**: no backend is `NOT_LINKED` from every verb.

## Do NOT

- Import the vendor module, or anything outside the standard library and the
  SDK's own tree.
- Declare a code here (rule 3, ADR 0160 §2).

## Verification

```sh
cd internal && go test -race ./service/proc/bpf/
```
