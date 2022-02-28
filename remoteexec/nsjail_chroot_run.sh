#!/bin/bash
set -e

if [[ "$WORK_DIR" == "" ]]; then
  echo "ERROR: WORK_DIR is not set" >&2
  exit 1
fi

rundir="$(pwd)"
chroot_workdir="/tmp/goma_chroot"

#
# mount directories under $chroot_workdir and execute.
#
run_dirs=($(ls -1 "$rundir"))
sys_dirs=(dev proc)

# RBE server generates __action_home__XXXXXXXXXX directory in $rundir
# (note: XXXXXXXXXX is a random).  Let's skip it because we do not use that.
# mount directories in the request.
for d in "${run_dirs[@]}"; do
  if [[ "$d" == __action_home__* ]]; then
    continue
  fi
  mkdir -p "$chroot_workdir/$d"
  mount --bind "$rundir/$d" "$chroot_workdir/$d"
done

# mount directories not included in the request.
for d in "${sys_dirs[@]}"; do
  # avoid to mount system directories if that exist in the user's request.
  if [[ -d "$rundir/$d" ]]; then
    continue
  fi
  # directory will be mounted by nsjail later.
  mkdir -p "$chroot_workdir/$d"
done
# needed to make nsjail bind device files.
touch "$chroot_workdir/dev/urandom"
touch "$chroot_workdir/dev/null"

# currently running with root. run the command with nobody:nogroup with chroot.
# We use nsjail to chdir without running bash script inside chroot, and
# libc inside chroot can be different from libc outside.
nsjail --quiet --config "$WORK_DIR/nsjail.cfg" -- "$@"
