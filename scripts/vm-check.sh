#!/bin/sh
# Runs inside a test VM as root, from cloud-init (or an init script under
# sysvinit), next to the hwspec binary under test. Installs hwspec where a
# user would, runs it the way a user would, and writes each step's exit code
# and output to the second serial port, where scripts/vm-run.sh reads them.
# POSIX sh and busybox only: no jq, bash or coreutils in the guest.

dir=$(dirname "$0")
out=/dev/ttyS1 # ttyS0 is the console; nothing else writes here
tmp=/tmp/hwspec-check
install -m 755 "$dir/hwspec" /usr/local/bin/hwspec

# step NAME COMMAND...: "==> NAME rc=N", stdout, then "==> NAME.stderr", stderr.
step() {
	name=$1
	shift
	"$@" >"$tmp.out" 2>"$tmp.err"
	printf '==> %s rc=%s\n' "$name" "$?"
	cat "$tmp.out"
	printf '==> %s.stderr\n' "$name"
	cat "$tmp.err"
}

# An ordinary user with a home (Arch's nobody is an expired account).
# su USER -c works with util-linux and busybox su.
id tester >/dev/null 2>&1 || useradd -m tester 2>/dev/null || adduser -D tester
user() { su -s /bin/sh tester -c "$*"; }

# PID 1's executable, and whether systemd is running (sd_booted's test).
pid1() {
	echo "exe=$(readlink /proc/1/exe)"
	if [ -d /run/systemd/system ]; then echo systemd=yes; else echo systemd=no; fi
}

{
	echo # firmware and GRUB write here too, without a final newline
	step pid pid1
	step version user /usr/local/bin/hwspec version
	step capture user /usr/local/bin/hwspec capture -f json
	step full /usr/local/bin/hwspec capture --full -f json # root: no pkexec
	printf '==> end\n'
} >"$out"
