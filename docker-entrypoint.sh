#!/usr/bin/env sh
set -e
if [ "$(id -u)" = 0 ]; then
  chown -R appuser:appuser /data
  exec gosu appuser "$@"
fi
exec "$@"
