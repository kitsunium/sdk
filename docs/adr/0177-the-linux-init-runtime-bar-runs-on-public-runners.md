# ADR 0177 — the Linux init runtime bar runs on public runners, and the lab is gone

- **Status**: Accepted
- **Date**: 2026-10-14
- **Accepted**: 2026-10-14
- **Deciders**: SDK maintainers
- **Amended**: 2026-10-14 (the same day): the residue Decision 3 deferred is built, and the lab is removed
- **Amends**: [ADR 0018](0018-sdk-cross-platform-portability.md) (§(b), the runtime bar: where it runs)
- **Related**: issue #118 (the lab legs that hung), [ADR 0095](0095-windows-runs-every-test-and-gates.md), [ADR 0144](0144-illumos-and-solaris-supervise-on-their-own-kernels-word.md)

## Context

ADR 0018 put the runtime bar on `e2e-vm.yml`: persistent hypervisor VMs of a
local lab, rolled back to a snapshot, driven over SSH from a self-hosted runner. It is
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
2. `e2e-vm.yml` stayed `workflow_dispatch`-only and kept the three Linux legs,
   for the one thing a container does not give: each distro's own kernel, booted
   with its own init from power-on. The Windows 11 and FreeBSD, OpenBSD, NetBSD
   legs were removed: `e2e-cross.yml` runs the same binaries on the same kernels.
3. The residue is migratable. Hosted runners expose `/dev/kvm` (seen on
   `e2e-cross` run 38526423869), so a QEMU guest booting a distro's cloud image
   is possible there.

## Amendment: the lab is removed

The owner decided that nothing of the SDK's CI stays on a local lab: the
repository is public, and its runtime bar must be reproducible by anyone from a
fork. Decision 3 is built, and Decision 2 is withdrawn: `e2e-vm.yml`, its
`kitsunium-runner` scale set and its four secrets are deleted, with every
reference to them (ADR 0018, ADR 0045, the workflow and e2e documentation).

**What replaces it.** `linux-init` gains three `vm` legs, `debian-kernel`,
`fedora-kernel` and `alpine-kernel`, on `ubuntu-latest`:

- the distro's official cloud image (Debian 13 `genericcloud` 20261001-2618,
  Fedora Cloud Base Generic 42-1.1, Alpine 3.22.6 `bios-cloudinit`), downloaded
  over HTTPS and refused unless its SHA-512 (SHA-256 for Fedora, the only digest
  its release publishes) equals the one pinned in the matrix: a republished image
  fails the leg instead of changing what is tested. The pins were copied from the
  distributors' own digest files (`SHA512SUMS`, `*-CHECKSUM`, `*.sha512`), not
  computed from the download;
- a copy-on-write overlay, so the verified file is never written;
- a cloud-init NoCloud seed (`cloud-localds`) that installs a throw-away ed25519
  key for root, generated per run;
- `qemu-system-x86_64 -enable-kvm` with user networking, ssh forwarded to
  loopback, the serial console in a file that is printed when the leg fails;
- the checkout shipped to the guest (some suites audit the sources above their
  working directory), then `bins/run.sh`, the script the container legs run:
  `systemd-run -p Delegate=yes` on the two systemd distros, a unified cgroup
  hierarchy with controllers enabled on Alpine's OpenRC.

The leg prints the runner's kernel and the guest's (`6.17.0-azure` against
`6.12.111+deb13-cloud-amd64` on the first green run), so a log says which kernel
the suites met.

**Limits, stated.**

- **Same CPU, not the same machine.** KVM runs the guest at hardware speed on the
  runner's CPU and under the runner's hypervisor (nested virtualisation, Azure).
  What is proved is the distro's kernel, init and userland; nothing is proved about
  other hardware, a bare-metal boot loader or a distro's own installer.
- **Cloud images, not installs.** The kernel is the one the distro ships in its
  cloud image (Debian's `-cloud` flavour, Alpine's `virt`, Fedora's stock kernel),
  which can differ from the one on a server of the same distro.
- **amd64 only.** Hosted runners expose KVM for x86-64 only; there is no arm64 leg.
- **A pin ages.** Fedora moves a release to its archive when it reaches end of life,
  and any pinned image may be withdrawn: the leg then fails at the download, loudly,
  and the URL and digest are bumped in the matrix.
- **Slower and flakier than a container.** A leg takes about two minutes when it
  works; a guest that never answers ssh is cut by the leg's own 7.5-minute wait,
  and the job's 30-minute cap bounds the rest.

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
  not seen by the container legs, which is what the three `*-kernel` legs are
  for. In a container systemd is the distro's, but it runs under the runner's
  cgroup hierarchy, not as the machine's first process.
- **Negative / accepted.** `linux-init` runs the unit suites as root; two suites
  that assert permission refusals must tolerate it.

## Alternatives considered

- **Leave `e2e-vm.yml` as the bar.** Rejected: manual, needs the lab, and its
  legs hung without output (#118).
- **A QEMU/KVM guest per distro on `ubuntu-latest`.** Adopted by the amendment.
- **Keep the lab for the kernels only.** Rejected by the owner: a bar that only a
  maintainer's infrastructure can run is not a bar a fork can reproduce.
- **Containers without a real init** (`sleep infinity` as PID 1). Rejected: the
  reaper and sd_notify rows are about what PID 1 does.
