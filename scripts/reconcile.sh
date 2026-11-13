#!/usr/bin/env bash
set -euo pipefail

ENV_FILE="${ENV_FILE:-/opt/llm-gateway/server.env}"

dsn=$(grep '^MYSQL_DSN=' "$ENV_FILE" | cut -d= -f2-)
db_user=$(printf '%s' "$dsn" | sed -E 's/^([^:]+):.*/\1/')
db_pass=$(printf '%s' "$dsn" | sed -E 's/^[^:]+:([^@]*)@.*/\1/')
db_host=$(printf '%s' "$dsn" | sed -E 's/.*@tcp\(([^:]+):[0-9]+\).*/\1/')
db_port=$(printf '%s' "$dsn" | sed -E 's/.*@tcp\([^:]+:([0-9]+)\).*/\1/')
db_name=$(printf '%s' "$dsn" | sed -E 's/.*\/([^?]+)\?.*/\1/')

run_sql() {
  MYSQL_PWD="$db_pass" mysql -h "$db_host" -P "$db_port" -u "$db_user" -N -B "$db_name" -e "$1"
}

drift=$(run_sql "
SELECT CONCAT(b.user_id, ' balance=', b.balance, ' ledger=', COALESCE(SUM(l.amount), 0))
FROM balances b
LEFT JOIN balance_logs l ON l.user_id = b.user_id
GROUP BY b.user_id, b.balance
HAVING b.balance <> COALESCE(SUM(l.amount), 0);
")

if [ -n "$drift" ]; then
  echo "$(date -Is) DRIFT"
  echo "$drift"
  exit 1
fi

users=$(run_sql "SELECT COUNT(*) FROM users;")
echo "$(date -Is) ok users=$users"
