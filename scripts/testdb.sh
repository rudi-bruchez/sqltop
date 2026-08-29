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

if ! podman container exists "$NAME"; then
  podman run -d --name "$NAME" \
    -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$PASSWORD" \
    -p "$PORT":1433 "$IMAGE" >/dev/null
elif [ "$(podman inspect -f '{{.State.Running}}' "$NAME")" != "true" ]; then
  podman start "$NAME" >/dev/null
fi

# Wait for the engine to accept connections rather than guessing at a sleep.
# The password goes through SQLCMDPASSWORD in the environment, not -P: a
# command-line argument is visible in ps to any local user on the machine.
for _ in $(seq 1 60); do
  if podman exec -e SQLCMDPASSWORD="$PASSWORD" "$NAME" \
      /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -C -Q "SELECT 1" >/dev/null 2>&1; then
    echo "export SQLTOP_TEST_DSN='sqlserver://sa:${PASSWORD}@127.0.0.1:${PORT}?encrypt=disable'"
    exit 0
  fi
  sleep 2
done

echo "the container did not become ready in two minutes" >&2
exit 1
