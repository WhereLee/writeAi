#!/usr/bin/env bash
set -eu

BACKUP_DIR="${BACKUP_DIR:-/opt/backup}"
ENV_FILE="${ENV_FILE:-/opt/llm-gateway/server.env}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"
mkdir -p "$BACKUP_DIR"
ts=$(date +%Y%m%d_%H%M%S)

dsn=$(grep '^MYSQL_DSN=' "$ENV_FILE" | cut -d= -f2-)
db_user=$(printf '%s' "$dsn" | sed -E 's/^([^:]+):.*/\1/')
db_pass=$(printf '%s' "$dsn" | sed -E 's/^[^:]+:([^@]*)@.*/\1/')
db_host=$(printf '%s' "$dsn" | sed -E 's/.*@tcp\(([^:]+):[0-9]+\).*/\1/')
db_port=$(printf '%s' "$dsn" | sed -E 's/.*@tcp\([^:]+:([0-9]+)\).*/\1/')
db_name=$(printf '%s' "$dsn" | sed -E 's/.*\/([^?]+)\?.*/\1/')

mysql_file="$BACKUP_DIR/mysql_$ts.sql.gz"
MYSQL_PWD="$db_pass" mysqldump -h "$db_host" -P "$db_port" -u "$db_user" --single-transaction --quick --no-tablespaces "$db_name" | gzip > "$mysql_file"
redis-cli --rdb "$BACKUP_DIR/redis_$ts.rdb" >/dev/null
find "$BACKUP_DIR" -type f -mtime +"$RETENTION_DAYS" -delete
echo "$(date -Is) backup ok mysql=$mysql_file redis=$BACKUP_DIR/redis_$ts.rdb"