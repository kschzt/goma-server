#!/bin/bash
export INPUT_ROOT="$(pwd)"
if [[ "$WORK_DIR" != "" ]]; then
  cd "${WORK_DIR}"
fi
export PWD="$(pwd)"
# exit 159 -> seccomp violation
nsjail -q -C "./nsjail.cfg" --cwd "$PWD" \
       --  \
       "$@"
