#!/usr/bin/env bats
# BATS tests for the network engines' merge gate (plan P8-infra): its path
# filter (net-gate-paths.sh) in throwaway repositories, its verdict
# (net-gate-verdict.sh) on every result GitHub reports, the guest boot and its
# one retry (boot-guest.sh) against stub qemu and ssh, the veth pair
# (net-veth.sh) against a stub ip, and the workflow's shape: its two events,
# no cancel on the queue, SHA-pinned actions, a read-only token, hosted
# runners only, every leg declaring its backends and capabilities, and the
# aggregator judging every need. Run by `make ci-scripts-check`.

setup() {
  CI_DIR="$(cd "$BATS_TEST_DIRNAME" && pwd)"
  REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
  WF="$REPO_ROOT/.github/workflows/net-kernel-gate.yml"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK"
}

# refute <command>... — fails when the command succeeds. A bare `! cmd` does
# not fail a bats test unless it is the test's last line.
refute() {
  if "$@"; then
    echo "refuted, yet it held: $*"
    return 1
  fi
}

# repo_with <path>... — a repository with a base commit and a second commit
# touching each path; prints nothing, leaves BASE and HEAD_SHA set.
repo_with() {
  cd "$WORK"
  git init -q .
  git config user.email t@example.com
  git config user.name t
  echo base >README.md
  git add README.md
  git commit -qm base
  BASE="$(git rev-parse HEAD)"
  for p in "$@"; do
    mkdir -p "$(dirname "$p")"
    echo change >>"$p"
    git add "$p"
  done
  git commit -qm change
  HEAD_SHA="$(git rev-parse HEAD)"
}

# ── net-gate-paths.sh ───────────────────────────────────────────────────────

@test "paths: a change to the loop engine is relevant" {
  repo_with internal/service/net/loop/core.go
  run bash "$CI_DIR/net-gate-paths.sh" "$BASE" "$HEAD_SHA"
  [ "$status" -eq 0 ]
  [ "${lines[-1]}" = "relevant=true" ]
}

@test "paths: the core, the kernel layer, the design, the facade, the tool and the gate's own files are relevant" {
  for p in internal/core/net/state.go internal/kernel/errs/e.go design/net.yaml pkg/v1/net/server/f.go \
    tools/netgate/gates.go go.mod internal/go.mod .github/workflows/net-kernel-gate.yml scripts/ci/boot-guest.sh; do
    rm -rf "$WORK" && mkdir -p "$WORK"
    repo_with "$p"
    run bash "$CI_DIR/net-gate-paths.sh" "$BASE" "$HEAD_SHA"
    [ "$status" -eq 0 ] || { echo "$p: $output"; false; }
    [ "${lines[-1]}" = "relevant=true" ] || { echo "$p: $output"; false; }
  done
}

@test "paths: a change elsewhere is not relevant" {
  repo_with docs/adr/0001.md internal/service/app/x.go design/sdk.yaml scripts/ci/go-modules.sh
  run bash "$CI_DIR/net-gate-paths.sh" "$BASE" "$HEAD_SHA"
  [ "$status" -eq 0 ]
  [ "${lines[-1]}" = "relevant=false" ]
}

# Fail-closed: a base the clone does not hold must never read as "nothing
# changed", which would skip the gate and report success.
@test "paths: an unknown base fails, never answers relevant=false" {
  repo_with internal/service/net/loop/core.go
  run bash "$CI_DIR/net-gate-paths.sh" 0123456789012345678901234567890123456789 "$HEAD_SHA"
  [ "$status" -eq 1 ]
  [[ "$output" != *"relevant="* ]]
}

@test "paths: bad usage is refused" {
  run bash "$CI_DIR/net-gate-paths.sh" only-one
  [ "$status" -eq 64 ]
  run bash "$CI_DIR/net-gate-paths.sh" "" x
  [ "$status" -eq 64 ]
}

# ── net-gate-verdict.sh ─────────────────────────────────────────────────────

@test "verdict: no network path changed passes with the lanes and the bench skipped" {
  run bash "$CI_DIR/net-gate-verdict.sh" success false skipped skipped
  [ "$status" -eq 0 ]
  [[ "$output" == *"counted as success"* ]]
}

@test "verdict: relevant and both passed passes" {
  run bash "$CI_DIR/net-gate-verdict.sh" success true success success
  [ "$status" -eq 0 ]
}

@test "verdict: relevant with a lane or the bench not passed fails, whatever it ended" {
  for r in failure cancelled skipped ""; do
    run bash "$CI_DIR/net-gate-verdict.sh" success true "$r" success
    [ "$status" -eq 1 ] || { echo "lanes '$r'"; false; }
    run bash "$CI_DIR/net-gate-verdict.sh" success true success "$r"
    [ "$status" -eq 1 ] || { echo "bench '$r'"; false; }
  done
}

@test "verdict: a path filter that did not succeed fails, even with relevant=false" {
  for r in failure cancelled skipped; do
    run bash "$CI_DIR/net-gate-verdict.sh" "$r" false skipped skipped
    [ "$status" -eq 1 ]
  done
  run bash "$CI_DIR/net-gate-verdict.sh" success "" skipped skipped
  [ "$status" -eq 1 ]
}

@test "verdict: bad usage is refused" {
  run bash "$CI_DIR/net-gate-verdict.sh" success true
  [ "$status" -eq 64 ]
}

# ── boot-guest.sh ───────────────────────────────────────────────────────────

# stubs <ssh-up-on-attempt> — qemu, qemu-img, ssh-keygen, cloud-localds and ssh
# stubs in $WORK/bin. qemu counts its starts in $WORK/qemu.starts and writes a
# pid file holding a sleeping process; ssh succeeds once qemu has started the
# given number of times (0: never).
stubs() {
  mkdir -p "$WORK/bin" "$WORK/vm"
  : >"$WORK/vm/base.qcow2"
  cat >"$WORK/bin/qemu" <<EOF
#!/usr/bin/env bash
echo start >>"$WORK/qemu.starts"
sleep 5 &
while [ \$# -gt 0 ]; do [ "\$1" = -pidfile ] && echo \$! >"\$2"; shift; done
EOF
  cat >"$WORK/bin/qemu-img" <<'EOF'
#!/usr/bin/env bash
[ "$1" = create ] && : >disk.qcow2
exit 0
EOF
  cat >"$WORK/bin/ssh-keygen" <<'EOF'
#!/usr/bin/env bash
echo key >key; echo ssh-ed25519 AAAA test >key.pub
EOF
  cat >"$WORK/bin/cloud-localds" <<'EOF'
#!/usr/bin/env bash
: >"$1"
EOF
  cat >"$WORK/bin/ssh" <<EOF
#!/usr/bin/env bash
n=\$(wc -l <"$WORK/qemu.starts" 2>/dev/null || echo 0)
[ "$1" -ne 0 ] && [ "\$n" -ge "$1" ]
EOF
  chmod +x "$WORK"/bin/*
  export QEMU="$WORK/bin/qemu" QEMU_IMG="$WORK/bin/qemu-img" SSH="$WORK/bin/ssh" \
    SSH_KEYGEN="$WORK/bin/ssh-keygen" LOCALDS="$WORK/bin/cloud-localds" BOOT_POLLS=3 BOOT_POLL_SECS=0
}

starts() { wc -l <"$WORK/qemu.starts" | tr -d ' '; }

@test "boot-guest: a guest up on the first boot is booted once" {
  stubs 1
  run bash "$CI_DIR/boot-guest.sh" "$WORK/vm"
  [ "$status" -eq 0 ]
  [ "$(starts)" -eq 1 ]
  [[ "$output" == *"up (attempt 1)"* ]]
  grep -q 'ssh-ed25519 AAAA test' "$WORK/vm/user-data"
}

@test "boot-guest: a first boot that never answers is retried once, on a fresh overlay" {
  stubs 2
  run bash "$CI_DIR/boot-guest.sh" "$WORK/vm"
  [ "$status" -eq 0 ]
  [ "$(starts)" -eq 2 ]
  [[ "$output" == *"attempt 1: the guest never answered ssh"* ]]
  [[ "$output" == *"up (attempt 2)"* ]]
}

@test "boot-guest: two failed boots fail, and no third is tried" {
  stubs 0
  run bash "$CI_DIR/boot-guest.sh" "$WORK/vm"
  [ "$status" -eq 1 ]
  [ "$(starts)" -eq 2 ]
  [[ "$output" == *"did not boot in 2 attempts"* ]]
  [ ! -e "$WORK/vm/qemu.pid" ]
}

@test "boot-guest: a directory without the image is refused" {
  stubs 1
  rm "$WORK/vm/base.qcow2"
  run bash "$CI_DIR/boot-guest.sh" "$WORK/vm"
  [ "$status" -eq 64 ]
  run bash "$CI_DIR/boot-guest.sh" "$WORK/none"
  [ "$status" -eq 64 ]
}

# ── net-veth.sh ─────────────────────────────────────────────────────────────

@test "veth: up builds the pair in its namespace on the benchmarking range and prints the bench's environment" {
  mkdir -p "$WORK/bin"
  printf '#!/usr/bin/env bash\necho "$*" >>"%s/ip.log"\n' "$WORK" >"$WORK/bin/ip"
  chmod +x "$WORK/bin/ip"
  IP="$WORK/bin/ip" run bash "$CI_DIR/net-veth.sh" up
  [ "$status" -eq 0 ]
  grep -qx 'netns add netgate-l3' "$WORK/ip.log"
  grep -qx 'link add ng-a type veth peer name ng-b' "$WORK/ip.log"
  grep -qx 'link set ng-b netns netgate-l3' "$WORK/ip.log"
  grep -qx -- '-n netgate-l3 addr add 198.18.0.2/30 dev ng-b' "$WORK/ip.log"
  [[ "$output" == *"KITSUNIUM_NETGATE_L3_PEER=198.18.0.2"* ]]
  [[ "$output" == *"KITSUNIUM_NETGATE_L3_LOCAL=198.18.0.1"* ]]
  [[ "$output" == *"KITSUNIUM_NETGATE_L3_EXEC=sudo $WORK/bin/ip netns exec netgate-l3"* ]]
}

@test "veth: up fails when ip fails; down succeeds with nothing to remove; bad usage is refused" {
  mkdir -p "$WORK/bin"
  printf '#!/usr/bin/env bash\nexit 2\n' >"$WORK/bin/ip"
  chmod +x "$WORK/bin/ip"
  IP="$WORK/bin/ip" run bash "$CI_DIR/net-veth.sh" up
  [ "$status" -ne 0 ]
  IP="$WORK/bin/ip" run bash "$CI_DIR/net-veth.sh" down
  [ "$status" -eq 0 ]
  run bash "$CI_DIR/net-veth.sh" sideways
  [ "$status" -eq 64 ]
}

# ── .github/workflows/net-kernel-gate.yml ───────────────────────────────────

@test "workflow: on every pull request push and in the merge queue" {
  grep -qE '^  pull_request:$' "$WF"
  grep -qE '^    types: \[opened, synchronize, reopened\]$' "$WF"
  grep -qE '^  merge_group:$' "$WF"
  grep -qE '^    types: \[checks_requested\]$' "$WF"
  refute grep -qE '^\s*pull_request_target:' "$WF"
  # No path filter at the trigger: a required check that never reports
  # blocks the merge, so the filter is a job and its skip reports success.
  refute grep -qE '^    paths(-ignore)?:' "$WF"
}

@test "workflow: a newer push cancels a pull request's run, never a queue entry's" {
  grep -qF 'cancel-in-progress: ${{ github.event_name == '"'"'pull_request'"'"' }}' "$WF"
}

@test "workflow: the filter compares the queue's base with the evaluated commit" {
  grep -qF 'BASE_SHA: ${{ github.event.pull_request.base.sha || github.event.merge_group.base_sha }}' "$WF"
  grep -qF 'EVAL_SHA: ${{ github.sha }}' "$WF"
  grep -qF 'bash scripts/ci/net-gate-paths.sh "$BASE_SHA" "$EVAL_SHA"' "$WF"
  grep -qF 'fetch-depth: 0' "$WF"
}

@test "workflow: every action pinned by a full SHA, a read-only token, hosted runners only" {
  [ "$(grep -cE '^\s+(- )?uses: ' "$WF")" -ge 4 ]
  [ -z "$(grep -E '^\s+(- )?uses: ' "$WF" | grep -vE 'uses: [^@]+@[0-9a-f]{40} # ')" ]
  grep -qE '^permissions:$' "$WF"
  grep -A1 -E '^permissions:$' "$WF" | grep -qx '  contents: read'
  [ -z "$(sed -n '/^permissions:/,/^[a-z]/p' "$WF" | grep -E 'write')" ]
  refute grep -qE '^\s+permissions:' "$WF"
  [ -z "$(grep -E '^\s+runs-on:' "$WF" | grep -v 'runs-on: ubuntu-latest$')" ]
}

@test "workflow: every leg declares requiredBackends and requiredCaps, non-empty" {
  legs="$(grep -cE '^          - leg: ' "$WF")"
  [ "$legs" -ge 2 ]
  [ "$(grep -cE '^            requiredBackends: "[a-z,]+"$' "$WF")" -eq "$legs" ]
  [ "$(grep -cE '^            requiredCaps: "[a-z,-]+"$' "$WF")" -eq "$legs" ]
  grep -qF 'requiredBackends=$REQUIRED_BACKENDS -X=$LOOP_PKG.requiredCaps=$REQUIRED_CAPS' "$WF"
}

@test "workflow: the guest boots through boot-guest.sh, the bench is judged by netgate on the evaluated commit" {
  grep -qF 'run: bash scripts/ci/boot-guest.sh vm' "$WF"
  grep -qF 'go run . -in "$RUNNER_TEMP/bench.txt" -sha "$EVAL_SHA"' "$WF"
  grep -qF 'name: net-bench-${{ github.sha }}' "$WF"
  grep -qF 'if [ "$head" != "$EVAL_SHA" ]; then' "$WF"
  refute grep -q 'continue-on-error' "$WF"
}

@test "workflow: the aggregator needs every job, always runs, and judges through the verdict" {
  grep -qE '^  net-kernel-gate:$' "$WF"
  grep -qF 'needs: [changes, lanes, bench]' "$WF"
  grep -qF 'if: ${{ always() }}' "$WF"
  grep -qF 'run: bash scripts/ci/net-gate-verdict.sh "$CHANGES" "$RELEVANT" "$LANES" "$BENCH"' "$WF"
}

@test "workflow: every job and step fits the 45-minute budget" {
  while IFS= read -r m; do
    [ "$m" -le 45 ] || { echo "timeout-minutes $m"; false; }
  done < <(grep -oE 'timeout-minutes: [0-9]+' "$WF" | awk '{ print $2 }')
  [ "$(grep -cE '^    timeout-minutes: ' "$WF")" -eq 4 ]
}
