#!/usr/bin/env bash
# Usage: scripts/vm-run.sh IMAGE BINARY [VERSION]
#        scripts/vm-run.sh --list          (the image names)
#        scripts/vm-run.sh --pin IMAGE     (url= and sum= lines, for CI)
#
# Boots IMAGE (a pinned cloud image, below) under KVM, runs the amd64 hwspec
# BINARY inside it with scripts/vm-check.sh, and checks the results: the
# version (when VERSION is given), PID 1, a user's capture (schema, VM
# detected, UEFI boot, the image's init, every PCI device named) and a root
# --full capture.
#
# Needs: /dev/kvm (read-write), qemu-system-x86_64, qemu-img, UEFI firmware
# ($OVMF_CODE and $OVMF_VARS, default: Ubuntu's ovmf package), genisoimage,
# curl, jq, sha256sum/sha512sum, timeout. Downloads are cached in $VM_CACHE
# (default ~/.cache/hwspec-vm); logs and captures go to $VM_OUT (default
# vm-out/IMAGE). Exits non-zero on any failed check.
set -euo pipefail

images="ubuntu-24.04 ubuntu-26.04 debian-13 fedora-44 arch alpine-3.24 debian-13-sysvinit"

# Pinned by URL and checksum: refreshing an image is a deliberate change
# here (vm-images.yml checks weekly that every URL still exists).
# init: os.init (PID 1's name); pid1: PID 1's executable (a regex);
# mode: cloud-init runs the check directly, or (sysvinit) switches init first.
pin() {
	case $1 in
	ubuntu-24.04)
		url=https://cloud-images.ubuntu.com/releases/24.04/release-20260926/ubuntu-24.04-server-cloudimg-amd64.img
		sum=sha256:6a81c37564db9b1ee84e141922625e1d7c5b389b99bb3c572e0243607d5bb4d2 ;;
	ubuntu-26.04)
		url=https://cloud-images.ubuntu.com/releases/26.04/release-20260927/ubuntu-26.04-server-cloudimg-amd64.img
		sum=sha256:8800651811af9a85465ad1d552add729947bb16488dddb4a9b5305a3d97332b2 ;;
	debian-13 | debian-13-sysvinit)
		url=https://cloud.debian.org/images/cloud/trixie/20261001-2618/debian-13-genericcloud-amd64-20261001-2618.qcow2
		sum=sha512:f46f0671a6e5bdec5291ab8972bae2f10e5408c2f64a74078f11efc2f06a436a9d0313ed50e0472542eeabf780e9f7c792ac0a314c6c20507fcd9fd81b468c3d ;;
	fedora-44)
		url=https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2
		sum=sha256:28680fe5b371a5a82ebf43a31926e086a168e59949d03969c5093e7071f90b7f ;;
	arch)
		url=https://geo.mirror.pkgbuild.com/images/v20261001.604814/Arch-Linux-x86_64-cloudimg-20261001.604814.qcow2
		sum=sha256:360f0fa49db6813bdc8e35bed230a2dc2ae3567b7b5ab74719c0a706e4e34e87 ;;
	alpine-3.24)
		url=https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/cloud/alpine-3.24.2-x86_64-cloudinit-r0.qcow2
		sum=sha512:c9504d23613f304e0cfb6f5fec872e29e5a5a62e2bc64796daf19c14fdddaa87dc252912fd8bdd17f7b8ebf0cd03305e4075993d54de175e6028d6b50414c67f ;;
	*)
		echo "unknown image '$1' (scripts/vm-run.sh --list)" >&2; exit 2 ;;
	esac
	init=systemd pid1='/systemd$' mode=direct
	case $1 in
	alpine-3.24) init=init pid1='^/bin/busybox$' ;; # OpenRC under busybox init
	debian-13-sysvinit) init=init pid1='/sbin/init$' mode=sysvinit ;;
	esac
}

case ${1:-} in
--list) tr ' ' '\n' <<<"$images"; exit 0 ;;
--pin) pin "${2:?image}"; printf 'url=%s\nsum=%s\n' "$url" "$sum"; exit 0 ;;
esac
image=${1:?image}; bin=${2:?binary}; want_version=${3:-}
pin "$image"

fail() { echo "FAIL ($image): $*" >&2; exit 1; }
[ -r /dev/kvm ] && [ -w /dev/kvm ] || fail "/dev/kvm is missing or not read-write for $(id -un)"
[ -f "$bin" ] || fail "no binary at $bin"
here=$(cd "$(dirname "$0")" && pwd)
cache=${VM_CACHE:-$HOME/.cache/hwspec-vm}
out=${VM_OUT:-vm-out/$image}
mkdir -p "$cache" "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# 1. The image, verified before every use (the cache may be restored from CI).
base=$cache/${url##*/}
verified() { echo "${sum#*:}  $base" | "${sum%%:*}sum" -c --quiet - >/dev/null 2>&1; }
if ! verified; then
	echo "Downloading $url"
	# HTTPS only, redirects included (Fedora's goes through mirrors).
	curl -fL --proto '=https' --proto-redir '=https' --retry 3 --no-progress-meter -o "$base.part" "$url"
	mv "$base.part" "$base"
	verified || fail "checksum mismatch for $url"
fi
qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$work/disk.qcow2" 12G

# 2. The NoCloud seed, which also carries the payload: binary and check script.
seed=$work/seed
mkdir "$seed"
install -m 755 "$bin" "$seed/hwspec"
install -m 755 "$here/vm-check.sh" "$seed/vm-check.sh"
printf 'instance-id: hwspec-%s\nlocal-hostname: hwspec-vm\n' "$image" >"$seed/meta-data"
mount_seed='mkdir -p /run/hwspec-seed && mount -o ro /dev/sr0 /run/hwspec-seed'
case $mode in
direct)
	# Arch's image waits for NTP before cloud-init's final stage
	# (systemd-time-wait-sync), forever without the outside world.
	cat >"$seed/user-data" <<-EOF
		#cloud-config
		bootcmd:
		  - [sh, -c, 'systemctl stop --no-block systemd-time-wait-sync.service 2>/dev/null || true']
		runcmd:
		  - [sh, -c, '$mount_seed; sh /run/hwspec-seed/vm-check.sh; poweroff']
	EOF
	;;
sysvinit)
	# Boot 1 (systemd): swap in sysvinit and an init script, then reboot.
	# Boot 2 (sysvinit): the init script runs the check and powers off.
	cat >"$seed/hwspec-check.init" <<-'EOF'
		#!/bin/sh
		### BEGIN INIT INFO
		# Provides:          hwspec-check
		# Required-Start:    $all
		# Required-Stop:
		# Default-Start:     2 3 4 5
		# Default-Stop:
		# Short-Description: hwspec CI check (scripts/vm-run.sh)
		### END INIT INFO
		[ "$1" = start ] || exit 0
		sh /usr/local/lib/hwspec-check/vm-check.sh
		poweroff &
	EOF
	cat >"$seed/switch.sh" <<-'EOF'
		#!/bin/sh
		set -e
		lib=/usr/local/lib/hwspec-check
		mkdir -p "$lib"
		cp /run/hwspec-seed/hwspec /run/hwspec-seed/vm-check.sh "$lib/"
		install -m 755 /run/hwspec-seed/hwspec-check.init /etc/init.d/hwspec-check
		apt-get update -q
		# systemd-sysv must go explicitly, and it's Protected (apt calls it
		# essential): sysvinit-core takes over as the init provider.
		DEBIAN_FRONTEND=noninteractive apt-get install -y -q --allow-remove-essential \
			sysvinit-core sysv-rc orphan-sysvinit-scripts systemd-sysv-
		update-rc.d hwspec-check defaults
	EOF
	# The switch's exit code goes to the payload port (after a newline:
	# firmware and GRUB leave it mid-line), so a failed switch fails after
	# boot 1 instead of timing out in a second systemd boot.
	cat >"$seed/user-data" <<-EOF
		#cloud-config
		runcmd:
		  - [sh, -c, '$mount_seed && sh /run/hwspec-seed/switch.sh; printf "\n==> switch rc=%s\n" \$? >/dev/ttyS1; reboot']
	EOF
	;;
esac
genisoimage -quiet -output "$work/seed.iso" -volid cidata -joliet -rock "$seed"

# 3. Boot under UEFI (Debian 13's image resets right after GRUB under
# SeaBIOS). -no-reboot makes a reboot end QEMU too, so each boot is one run.
# No VGA: QEMU's (1234:1111) isn't in pci.ids, and every device is named.
# The outside world only for apt (switching to sysvinit); otherwise a NIC
# with restrict=on: DHCP works, nothing leaves the VM (without a NIC at
# all, cloud-init waits minutes for the network, and Arch's forever).
cp "${OVMF_VARS:-/usr/share/OVMF/OVMF_VARS_4M.fd}" "$work/vars.fd" # boot entries persist across boots
boot() { # LOG NIC
	timeout 600 qemu-system-x86_64 -enable-kvm -machine q35 -cpu host -smp 2 -m 2048 \
		-drive "if=pflash,format=raw,readonly=on,file=${OVMF_CODE:-/usr/share/OVMF/OVMF_CODE_4M.fd}" \
		-drive "if=pflash,format=raw,file=$work/vars.fd" \
		-display none -vga none -no-reboot \
		-serial "file:$1" -serial "file:$work/payload.log" \
		-drive "if=virtio,format=qcow2,file=$work/disk.qcow2" \
		-cdrom "$work/seed.iso" \
		-nic "$2" ||
		fail "QEMU failed or timed out after 10 minutes; see $1"
}
start=$SECONDS
if [ "$mode" = sysvinit ]; then
	echo "Boot 1/2 ($image): switching to sysvinit"
	boot "$out/console-1.log" user,model=virtio-net-pci
	tr -d '\r' <"$work/payload.log" >"$out/switch.log"
	grep -qx '==> switch rc=0' "$out/switch.log" || {
		tail -n 40 "$out/console-1.log" >&2
		fail "switching to sysvinit failed ($(grep -a '^==> ' "$out/switch.log" || echo 'no exit code')); console log above"
	}
	echo "Boot 2/2 ($image): running the check under sysvinit"
	boot "$out/console.log" user,model=virtio-net-pci,restrict=on
	grep -aq 'INIT: version' "$out/console.log" || fail "boot 2 didn't start sysvinit (no 'INIT: version' on the console)"
else
	echo "Booting $image"
	boot "$out/console.log" user,model=virtio-net-pci,restrict=on
fi
echo "VM ran for $((SECONDS - start)) s"

# 4. Split the payload log into one file per step, then check. Step names
# come from the guest: anything but [a-z.] is refused, not used as a path.
tr -d '\r' <"$work/payload.log" >"$out/payload.log"
awk -v d="$out" '
	/^==> / {
		if ($2 !~ /^[a-z.]+$/) { print "bad step name: " $2 > "/dev/stderr"; exit 1 }
		f = d "/" $2; printf "" > f; next
	}
	f { print > f }' "$out/payload.log" || fail "the payload log has a bad step marker"
grep -qx '==> end' "$out/payload.log" || {
	tail -n 40 "$out/console.log" >&2
	fail "the check didn't finish (console log above)"
}
rc() { sed -n "s/^==> $1 rc=//p" "$out/payload.log"; }
for s in pid version capture full; do
	[ "$(rc $s)" = 0 ] || { cat "$out/$s.stderr" >&2; fail "'$s' exited $(rc $s)"; }
done

# PID 1 is the init system the image is meant to test, not just its name.
exe=$(sed -n 's/^exe=//p' "$out/pid")
grep -Eq "$pid1" <<<"$exe" || fail "PID 1 is '$exe', want /$pid1/"
want_systemd=no; [ "$init" = systemd ] && want_systemd=yes
grep -qx "systemd=$want_systemd" "$out/pid" || fail "/run/systemd/system: want systemd=$want_systemd, got $(grep '^systemd=' "$out/pid")"

version=$(cat "$out/version")
if [ -n "$want_version" ]; then
	[ "$version" = "hwspec $want_version" ] || fail "version '$version' isn't $want_version"
fi
jq --arg init "$init" '{
	schema: (.schema_version == 1),
	vm: (.os.virtualization == "vm"),
	uefi: (.os.boot_mode == "uefi"),
	init: (.os.init == $init),
	memory: (.memory.total_bytes > 0),
	pci_named: ((.pci | length) > 0 and all(.pci[]; (.identity.vendor // "") != "")),
	unprivileged: (.privileged == false),
	os: .os.pretty_name, os_init: .os.init, kernel: .os.kernel, warnings: .warnings
}' "$out/capture" | tee "$out/checks.json"
jq -e '[.schema, .vm, .uefi, .init, .memory, .pci_named, .unprivileged] | all' "$out/checks.json" >/dev/null ||
	fail "capture checks failed (above)"
jq -e '.privileged == true and .schema_version == 1' "$out/full" >/dev/null ||
	fail "the root --full capture isn't privileged"
mv "$out/capture" "$out/capture.json"
mv "$out/full" "$out/full.json"
echo "PASS ($image): $version, PID 1 $exe ($(jq -r .os_init "$out/checks.json")), $(jq -r .os "$out/checks.json")"
