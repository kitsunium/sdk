#!/usr/bin/env bash
# scripts/ci/boot-guest.sh — boot a distro cloud image under QEMU/KVM and wait
# for its ssh, retrying ONCE when the guest never comes up.
#
# A guest that does not boot is the runner's flake, not the change's: the
# boot hang e2e-cross.yml measured comes and goes with the host. So a boot
# that never answers ssh is torn down — qemu killed, the overlay disk
# recreated from the pristine base — and tried a second time; a second
# failure fails. The retry covers the boot only: what runs in the guest
# afterwards is never retried here.
#
# DIR must hold base.qcow2 (the verified image). The script writes key and
# key.pub (the ssh key), seed.iso (cloud-init: the key for the image's
# default user and for root, no password), disk.qcow2 (the overlay),
# console.log and qemu.pid, and forwards host port SSH_PORT to the guest's 22.
#
# Usage: boot-guest.sh DIR
# Environment: GUEST_USER (root), SSH_PORT (2222), BOOT_POLLS (60) and
#   BOOT_POLL_SECS (5) per attempt; QEMU, QEMU_IMG, SSH, SSH_KEYGEN and
#   LOCALDS name the commands (the suite stubs them).
# Exit 0 once ssh answers, 1 after the second failed boot, 64 on bad usage.

set -euo pipefail

if [ "$#" -ne 1 ] || [ ! -d "$1" ]; then
  echo "usage: $0 DIR (an existing directory holding base.qcow2)" >&2
  exit 64
fi
dir="$1"
if [ ! -r "$dir/base.qcow2" ]; then
  echo "boot-guest: $dir/base.qcow2 is missing" >&2
  exit 64
fi

user="${GUEST_USER:-root}"
port="${SSH_PORT:-2222}"
polls="${BOOT_POLLS:-60}"
every="${BOOT_POLL_SECS:-5}"
qemu="${QEMU:-qemu-system-x86_64}"
qemu_img="${QEMU_IMG:-qemu-img}"
ssh_cmd="${SSH:-ssh}"
keygen="${SSH_KEYGEN:-ssh-keygen}"
localds="${LOCALDS:-cloud-localds}"
# One boot, then one retry: fixed, not a knob.
attempts=2

cd "$dir"
rm -f key key.pub
"$keygen" -q -t ed25519 -N '' -f key
{
  echo '#cloud-config'
  echo 'disable_root: false'
  echo 'ssh_pwauth: false'
  echo 'ssh_authorized_keys:'
  echo "  - $(cat key.pub)"
  echo 'users:'
  echo '  - default'
  echo '  - name: root'
  echo '    ssh_authorized_keys:'
  echo "      - $(cat key.pub)"
} >user-data
printf 'instance-id: sdk-net-gate\nlocal-hostname: sdk-net-gate\n' >meta-data
"$localds" seed.iso user-data meta-data

ssh_up() {
  "$ssh_cmd" -i key -p "$port" -o BatchMode=yes -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 "$user@127.0.0.1" true 2>/dev/null
}

stop_guest() {
  if [ -s qemu.pid ]; then
    kill "$(cat qemu.pid)" 2>/dev/null || true
  fi
  rm -f qemu.pid
}

attempt=1
while [ "$attempt" -le "$attempts" ]; do
  echo "boot-guest: attempt $attempt of $attempts"
  stop_guest
  rm -f disk.qcow2 console.log
  "$qemu_img" create -q -f qcow2 -F qcow2 -b base.qcow2 disk.qcow2
  # Room for the checkout: cloud-init grows the root partition into it.
  "$qemu_img" resize -q disk.qcow2 8G
  "$qemu" -enable-kvm -cpu host -smp 2 -m 3072 \
    -display none -serial file:console.log -daemonize -pidfile qemu.pid \
    -drive file=disk.qcow2,if=virtio -drive file=seed.iso,if=virtio,format=raw,readonly=on \
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$port-:22" -device virtio-net-pci,netdev=n0
  i=0
  while [ "$i" -lt "$polls" ]; do
    if ssh_up; then
      echo "boot-guest: the guest is up (attempt $attempt)"
      exit 0
    fi
    sleep "$every"
    i=$((i + 1))
  done
  echo "boot-guest: attempt $attempt: the guest never answered ssh" >&2
  tail -40 console.log >&2 2>/dev/null || true
  attempt=$((attempt + 1))
done
stop_guest
echo "::error::boot-guest: the guest did not boot in $attempts attempts"
exit 1
