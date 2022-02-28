#!/bin/bash
set -e
if [[ "$WORK_DIR" != "" ]]; then
  cd "${WORK_DIR}"
fi
exec "$@"
