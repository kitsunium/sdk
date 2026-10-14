# ADR 0177 — the Linux init runtime bar runs on public runners, and the lab keeps only what a container cannot give

- **Status**: Accepted
- **Date**: 2026-10-14
- **Accepted**: 2026-10-14
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0018](0018-sdk-cross-platform-portability.md) (§(b), the runtime bar: where it runs)
- **Related**: issue #118 (the lab legs that hung), [ADR 0095](0095-windows-runs-every-test-and-gates.md), [ADR 0144](0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md)

## Context

ADR 0018 put the runtime bar on `e2e-vm.yml`: persistent Proxmox VMs of the
lab, rolled back to a snapshot, driven over SSH from a self-hosted runner. It is
manual (`workflow_dispatch`) because it needs the lab and four secrets, so for
months the bar that proves `cgroup`, `reaper` and `sd_notify` behave on a real
init ran only when somebody remembered. The repository is public: GitHub-hosted
minutes cost nothing.

`e2e-cross.yml` had already moved Linux, macOS, Windows, the three BSDs, illumos
and Solaris to hosted runners. What it did not cover, and `e2e-vm.yml` did:

1. **Root with a delegated cgroup v2 subtree.** `cgroup.Available` is an honest
   write probe; the unprivileged `native` Linux leg reports `NotSupported` for
   every cgroup row, so the cgroup conformance never ran there.
2. **A real init as PID 1**: systemd (Debian, Fedora) and OpenRC (Alpine).
3. **The distro's userland** (glibc, musl).
4. **The distro's own kernel.**

Windows 11 and the three BSDs of `e2e-vm.yml` ran the same binaries as
`e2e-cross.yml`'s `native` Windows leg and `bsd` job. The SDK carries no Windows
service (SCM) code, so there is no SCM behaviour for either to prove.

## Decision

1. `e2e-cross.yml` gains the **`linux-init`** job, four legs on `ubuntu-latest`,
   each running the conformance binary and the platform-sensitive suites as root
   inside a delegated cgroup:
   - `ubuntu-systemd`: the runner itself, through
     `systemd-run -p Delegate=yes`;
   - `debian-systemd`, `fedora-systemd`: a privileged container whose PID 1 is
     the distro's systemd, the suites run through `systemd-run -p Delegate=yes`;
   - `alpine-openrc`: a privileged container whose PID 1 is busybox init running
     OpenRC, the container's processes moved into a leaf cgroup and the
     controllers enabled, as a container entrypoint does.
   The leg prints the kernel, PID 1, the cgroup the suites sit in and the distro,
   so a green run says what it ran on.
2. `e2e-vm.yml` stays `workflow_dispatch`-only and keeps **only** the three Linux
   legs, for the one thing a container does not give: each distro's own kernel,
   booted with its own init from power-on. The Windows 11 and FreeBSD, OpenBSD,
   NetBSD legs are removed: `e2e-cross.yml` runs the same binaries on the same
   kernels.
3. The residue is migratable. Hosted runners expose `/dev/kvm` (seen on
   `e2e-cross` run 38526423869), so a QEMU guest booting a distro's cloud image
   is possible there. It is not built; when it is, `e2e-vm.yml` and the lab
   secrets go.

## Consequences

- **Positive.** The first run of `linux-init` found a defect the lab legs had
  been reporting as a hang since #118: a spawn that asks for a cgroup placement
  and nothing else encodes an empty trampoline payload, and
  `installTrampoline` read an empty value as "not a trampoline", so the child
  ran the host program again and the parent blocked on its handshake for ever
  (`internal/service/proc/exec`, fixed in the same change: the variable's
  presence decides). It also found a unit test that assumed no host delegates
  `/sys/fs/cgroup/` (`cgroup.Test_WithRoot`).
- **Positive.** Every push runs the bar, not a person's memory of it.
- **Negative / accepted.** A container shares the runner's kernel (Azure, 6.17
  on the first runs): a divergence specific to another distro's kernel config is
  not seen until `e2e-vm.yml` is dispatched by hand. systemd is the distro's, but
  it runs under the runner's cgroup hierarchy, not as the machine's first
  process.
- **Negative / accepted.** `linux-init` runs the unit suites as root; two suites
  that assert permission refusals must tolerate it (the lab ran as root too).

## Alternatives considered

- **Leave `e2e-vm.yml` as the bar.** Rejected: manual, needs the lab, and its
  legs hung without output (#118).
- **A QEMU/KVM guest per distro on `ubuntu-latest`.** Not rejected, deferred:
  it needs an image pipeline (download, cloud-init, SSH) that nobody has built;
  see Decision 3.
- **Containers without a real init** (`sleep infinity` as PID 1). Rejected: the
  reaper and sd_notify rows are about what PID 1 does.
