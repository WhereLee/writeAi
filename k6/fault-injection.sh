#!/usr/bin/env bash
set -u

BASE="${BASE:-http://localhost:8080}"
API_KEY="${API_KEY:-}"
MODEL="${MODEL:-deepseek-chat}"
USER_ID="${USER_ID:-2}"
REDIS_SERVICE="${REDIS_SERVICE:-redis-server}"
MYSQL_SERVICE="${MYSQL_SERVICE:-mysql}"

need_key() {
  if [ -z "$API_KEY" ]; then
    echo "Set API_KEY before running this scenario" >&2
    exit 1
  fi
}

request_gateway() {
  curl -s -o /dev/null -w '%{http_code}' "$BASE/v1/chat/completions" \
    --max-time 40 \
    -X POST \
    -H "Content-Type: application/json" \
    -H "X-API-Key: $API_KEY" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"test\"}]}"
}

scenario="${1:-}"
case "$scenario" in
  "upstream-timeout")
    need_key
    echo "=== Scenario: Upstream Timeout ==="
    echo "Before: balance = $(redis-cli GET balance:$USER_ID)"
    target=$(dig +short api.deepseek.com | head -1)
    if [ -z "$target" ]; then
      echo "could not resolve api.deepseek.com" >&2
      exit 1
    fi
    cleanup_upstream() {
      iptables -D OUTPUT -d "$target" -j DROP 2>/dev/null || true
    }
    trap cleanup_upstream EXIT
    iptables -A OUTPUT -d "$target" -j DROP
    start=$(date +%s%N)
    status=$(request_gateway)
    end=$(date +%s%N)
    echo "status=$status latency=$(( (end - start) / 1000000 ))ms"
    echo "After: balance = $(redis-cli GET balance:$USER_ID)"
    echo "Recovery status=$(request_gateway)"
    echo "Final: balance = $(redis-cli GET balance:$USER_ID)"
    ;;

  "redis-down")
    need_key
    echo "=== Scenario: Redis Down ==="
    if ! systemctl is-active --quiet "$REDIS_SERVICE"; then
      echo "$REDIS_SERVICE is not active" >&2
      exit 1
    fi
    systemctl stop "$REDIS_SERVICE"
    sleep 1
    echo "status=$(request_gateway)"
    systemctl start "$REDIS_SERVICE"
    sleep 2
    echo "health=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz")"
    ;;

  "mysql-down")
    echo "=== Scenario: MySQL Down ==="
    if ! systemctl is-active --quiet "$MYSQL_SERVICE"; then
      echo "$MYSQL_SERVICE is not active" >&2
      exit 1
    fi
    systemctl stop "$MYSQL_SERVICE"
    sleep 1
    echo "login=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/login" -X POST -H "Content-Type: application/json" -d '{"username":"testuser","password":"testpass123"}')"
    systemctl start "$MYSQL_SERVICE"
    sleep 3
    echo "login_after_recovery=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/login" -X POST -H "Content-Type: application/json" -d '{"username":"testuser","password":"testpass123"}')"
    ;;

  "concurrent-balance")
    need_key
    echo "=== Scenario: Concurrent Balance Check ==="
    redis-cli SET "balance:$USER_ID" 20
    for i in 1 2 3 4 5; do
      (code=$(request_gateway); echo "$code") &
    done
    wait
    echo "Final balance: $(redis-cli GET balance:$USER_ID)"
    ;;

  "rate-limit")
    need_key
    echo "=== Scenario: Rate Limiting ==="
    tmp=$(mktemp)
    for i in $(seq 1 120); do
      (code=$(request_gateway); echo "$code" >> "$tmp") &
    done
    wait
    sort "$tmp" | uniq -c
    rm -f "$tmp"
    ;;

  *)
    echo "Usage: bash fault-injection.sh <scenario>" >&2
    echo "Scenarios: upstream-timeout, redis-down, mysql-down, concurrent-balance, rate-limit" >&2
    exit 1
    ;;
esac
