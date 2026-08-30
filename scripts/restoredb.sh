#!/usr/bin/env bash
# Restore the demonstration database into the test container, so sqlstress has
# something to load and sqltop has something to show. Usage:
#
#   scripts/restoredb.sh [path-to-backup]
#
# Idempotent: restoring again over an existing copy is the way to undo whatever
# a demonstration did to it.
set -euo pipefail

BAK="${1:-$HOME/TresoritDrive/Databases/sqlserver/PachadataFormation2019_BASE.bak}"
NAME="${SQLTOP_TEST_CONTAINER:-sqltop-test}"
PASSWORD="${SQLTOP_TEST_PASSWORD:-Sqltop_dev_2026!}"
DB="${SQLTOP_DEMO_DB:-PachadataFormation}"

if [ ! -f "$BAK" ]; then
  echo "no backup at $BAK" >&2
  exit 1
fi

# The container must be up; testdb.sh is what knows how to get it there.
"$(dirname "$0")/testdb.sh" >/dev/null

podman exec "$NAME" mkdir -p /var/opt/mssql/backup
podman cp "$BAK" "$NAME":/var/opt/mssql/backup/demo.bak

# The backup was taken on Windows, so both files need moving onto the Linux
# data directory. Their logical names are read from the backup rather than
# assumed, so a different backup still works.
sql() {
  SQLCMDPASSWORD="$PASSWORD" podman exec -i -e SQLCMDPASSWORD "$NAME" \
    /opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -C -b -h -1 -W "$@"
}

FILES=$(sql -s'|' -Q "SET NOCOUNT ON; RESTORE FILELISTONLY FROM DISK='/var/opt/mssql/backup/demo.bak'")
DATA=$(echo "$FILES" | awk -F'|' '$3=="D"{print $1; exit}')
LOG=$(echo "$FILES" | awk -F'|' '$3=="L"{print $1; exit}')
if [ -z "$DATA" ] || [ -z "$LOG" ]; then
  echo "could not read the file list out of the backup" >&2
  exit 1
fi

sql -Q "RESTORE DATABASE [$DB] FROM DISK='/var/opt/mssql/backup/demo.bak'
  WITH MOVE '$DATA' TO '/var/opt/mssql/data/${DB}.mdf',
       MOVE '$LOG' TO '/var/opt/mssql/data/${DB}_log.ldf',
       REPLACE, RECOVERY" >/dev/null

echo "restored $DB into container $NAME"
