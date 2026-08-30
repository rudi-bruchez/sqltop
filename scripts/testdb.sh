#!/usr/bin/env bash
# Start or wake the SQL Server container used by the integration tests, then
# print the export line. Usage: eval "$(scripts/testdb.sh)"
#
# The container is the tool's own, named sqltop-test, created on first use with
# the password below. It deliberately does not reuse whatever SQL Server
# containers happen to exist on the machine: the tests must provision what they
# need rather than depend on someone's local state.
set -euo pipefail

NAME="${SQLTOP_TEST_CONTAINER:-sqltop-test}"
IMAGE="${SQLTOP_TEST_IMAGE:-mcr.microsoft.com/mssql/server:2022-latest}"
PORT="${SQLTOP_TEST_PORT:-11433}"
PASSWORD="${SQLTOP_TEST_PASSWORD:-Sqltop_dev_2026!}"

# The password reaches podman through podman's own environment and is
# forwarded by naming the variable alone on -e, here and on the exec below.
# -e NAME=value would put the literal password in podman's argv, which ps
# shows to any local user on the machine.
if ! podman container exists "$NAME"; then
  MSSQL_SA_PASSWORD="$PASSWORD" podman run -d --name "$NAME" \
    -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD \
    -p "$PORT":1433 "$IMAGE" >/dev/null
elif [ "$(podman inspect -f '{{.State.Running}}' "$NAME")" != "true" ]; then
  podman start "$NAME" >/dev/null
fi

# Wait for the engine to accept connections rather than guessing at a sleep.
# The DSN printed on success carries the password, which is the point of the
# script: it is meant to be eval'd into a shell, and the container is a local
# throwaway with a password that is in this file anyway.
for _ in $(seq 1 60); do
  if SQLCMDPASSWORD="$PASSWORD" podman exec -e SQLCMDPASSWORD "$NAME" \
      /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -C -Q "SELECT 1" >/dev/null 2>&1; then
    echo "export SQLTOP_TEST_DSN='sqlserver://sa:${PASSWORD}@127.0.0.1:${PORT}?encrypt=disable'"
    exit 0
  fi
  sleep 2
done

echo "the container did not become ready in two minutes" >&2
exit 1
