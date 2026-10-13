#!/usr/bin/env bash
# inner.sh — runs inside the privileged container started by run.sh.
set -euo pipefail
trap 'echo "ERROR line $LINENO: $BASH_COMMAND" >&2' ERR
iters="$1"
negative="${2:-0}"
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq cryptsetup-bin dmsetup e2fsprogs procps >/dev/null 2>&1

# A previous run killed mid-way leaves its mappings in the VM: drop them.
umount -l /mnt 2>/dev/null || true
dmsetup remove kcons-cut 2>/dev/null || true
cryptsetup close kcons-crypt 2>/dev/null || true

img=/tmp/disk.img
truncate -s 8G "$img"
loop="$(losetup -f --show "$img")"
cleanup() {
	umount -l /mnt 2>/dev/null || true
	dmsetup remove kcons-cut 2>/dev/null || true
	cryptsetup close kcons-crypt 2>/dev/null || true
	losetup -d "$loop" 2>/dev/null || true
}
trap cleanup EXIT

echo -n pw | cryptsetup luksFormat -q --type luks2 --pbkdf pbkdf2 --pbkdf-force-iterations 1000 "$loop" -
echo -n pw | cryptsetup open "$loop" kcons-crypt --key-file -
sectors="$(blockdev --getsz /dev/mapper/kcons-crypt)"
linear="0 $sectors linear /dev/mapper/kcons-crypt 0"
cut="0 $sectors error"
dmsetup create --noudevsync kcons-cut --table "$linear"
# No udev in the container: the node is made by hand.
dmsetup mknodes kcons-cut
mkfs.ext4 -q /dev/mapper/kcons-cut
mkdir -p /mnt
mount /dev/mapper/kcons-cut /mnt
echo "SETUP $(cryptsetup status kcons-crypt | grep -E 'cipher|keysize' | tr -s ' ' | tr '\n' ' ')"

export DMCRYPT_DIR=/mnt
[ "$negative" = 1 ] || /work/consensus.test -test.run '^TestDMCryptBench$' -test.count=1 | grep '^BENCH'

pass=0
for i in $(seq 1 "$iters"); do
	# Every third iteration stops the writer right after a rotation's seal
	# and cuts there (review H2); the others cut at a random instant.
	atseal=0
	noseal=0
	[ "$negative" = 1 ] || [ $((i % 3)) != 0 ] || atseal=1
	DMCRYPT_ACK_BEFORE_SYNC="$negative" DMCRYPT_STOP_AT_SEAL="$atseal" \
		/work/consensus.test -test.run '^TestDMCryptWriter$' >/tmp/acked.log 2>&1 &
	w=$!
	if [ "$atseal" = 1 ]; then
		for _ in $(seq 1 600); do
			grep -q '^SEALED' /tmp/acked.log 2>/dev/null && break
			sleep 0.05
		done
		# A seal-cut iteration that never reached a seal did not test the
		# seal: it fails rather than passing as a random cut.
		grep -q '^SEALED' /tmp/acked.log || { echo "CRASH $i no rotation reached"; noseal=1; }
	else
		# Let it reopen the log and acknowledge, then cut at a random instant.
		for _ in $(seq 1 200); do
			grep -q '^SYNCED' /tmp/acked.log 2>/dev/null && break
			sleep 0.05
		done
		sleep "0.$((RANDOM % 9 + 1))"
	fi
	# Power cut: every write not yet on the crypt device is lost.
	dmsetup suspend --noflush --nolockfs kcons-cut
	dmsetup load kcons-cut --table "$cut"
	dmsetup resume kcons-cut
	kill -9 "$w" 2>/dev/null || true
	wait "$w" 2>/dev/null || true
	acked="$({ grep '^SYNCED' /tmp/acked.log || true; } | tail -1 | cut -d' ' -f2)"
	acked="${acked:-0}"
	umount -l /mnt 2>/dev/null || true
	sync 2>/dev/null || true
	echo 3 >/proc/sys/vm/drop_caches
	dmsetup suspend kcons-cut
	dmsetup load kcons-cut --table "$linear"
	dmsetup resume kcons-cut
	mount /dev/mapper/kcons-cut /mnt
	if line="$(DMCRYPT_ACKED="$acked" /work/consensus.test -test.run '^TestDMCryptVerify$' -test.count=1 2>&1)" && [ "$noseal" = 0 ]; then
		echo "CRASH $i at_seal=$atseal $(echo "$line" | grep '^VERIFY')"
		pass=$((pass + 1))
	else
		echo "CRASH $i FAILED at_seal=$atseal no_seal=$noseal acked=$acked"
		echo "$line" | tail -5
	fi
done
echo "RESULT crash_iterations=$iters passed=$pass negative_control=$negative"
if [ "$negative" = 1 ]; then
	# The control must be caught: some acknowledged entry lost.
	[ "$pass" != "$iters" ]
else
	[ "$pass" = "$iters" ]
fi
