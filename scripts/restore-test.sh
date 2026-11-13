#!/usr/bin/env bash
set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/opt/backup}"
TEST_DB="${TEST_DB:-llm_gateway_restore_test}"
MYSQL_ROOT_USER="${MYSQL_ROOT_USER:-root}"

latest=$(ls -1t "$BACKUP_DIR"/mysql_*.sql.gz 2>/dev/null | head -n 1)
if [ -z "$latest" ]; then
  echo "no backup found in $BACKUP_DIR" >&2
  exit 1
fi

mysql_root() {
  # Run as root on the host; Ubuntu's MySQL root uses auth_socket.
  mysql -u "$MYSQL_ROOT_USER" "$@"
}

mysql_root -e "DROP DATABASE IF EXISTS \`$TEST_DB\`; CREATE DATABASE \`$TEST_DB\` CHARACTER SET utf8mb4;"
gunzip -c "$latest" | mysql_root "$TEST_DB"

users=$(mysql_root -N -B "$TEST_DB" -e "SELECT COUNT(*) FROM users;")
logs=$(mysql_root -N -B "$TEST_DB" -e "SELECT COUNT(*) FROM balance_logs;")
drift=$(mysql_root -N -B "$TEST_DB" -e "SELECT COUNT(*) FROM (SELECT b.user_id FROM balances b LEFT JOIN balance_logs l ON l.user_id = b.user_id GROUP BY b.user_id, b.balance HAVING b.balance <> COALESCE(SUM(l.amount), 0)) t;")

echo "restored=$latest users=$users balance_logs=$logs drift_users=$drift"
mysql_root -e "DROP DATABASE \`$TEST_DB\`;"
