# 故障注入场景脚本（在服务器上执行）
# 用法: bash fault-injection.sh <scenario>

BASE=http://localhost:8080
API_KEY="sk-8c7c47bd-cf4c-4c81-81ac-efc23f0a"

scenario="$1"
case "$scenario" in
  "upstream-timeout")
    echo "=== Scenario: Upstream Timeout ==="
    echo "Simulating: kill network to upstream (iptables DROP)"
    echo "Expected: gateway returns 502, refund tokens, recover after"
    echo ""
    echo "Before: balance = $(redis-cli GET balance:2)"
    echo "Dropping upstream traffic..."
    iptables -A OUTPUT -d $(dig +short api.deepseek.com | head -1) -j DROP
    echo "Sending request..."
    start=$(date +%s%N)
    curl -s -o /dev/null -w '%{http_code}' $BASE/v1/chat/completions -X POST -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}]}'
    end=$(date +%s%N)
    echo " latency: $(( (end - start) / 1000000 ))ms"
    echo "After: balance = $(redis-cli GET balance:2)"
    echo "Removing iptables rule..."
    iptables -D OUTPUT -d $(dig +short api.deepseek.com | head -1) -j DROP
    echo "Recovery check..."
    curl -s -o /dev/null -w '%{http_code}' $BASE/v1/chat/completions -X POST -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}]}'
    echo ""
    echo "Final: balance = $(redis-cli GET balance:2)"
    ;;

  "redis-down")
    echo "=== Scenario: Redis Down ==="
    echo "Simulating: systemctl stop redis-server"
    echo "Expected: billing fails gracefully, rate limit falls back, recovers"
    echo ""
    systemctl stop redis-server
    sleep 1
    echo "Sending request while Redis is down..."
    status=$(curl -s -o /dev/null -w '%{http_code}' $BASE/v1/chat/completions -X POST -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}]}')
    echo "Status: $status"
    echo "Restarting Redis..."
    systemctl start redis-server
    sleep 2
    echo "Recovery..."
    curl -s -o /dev/null -w '%{http_code}' $BASE/health
    echo ""
    ;;

  "mysql-down")
    echo "=== Scenario: MySQL Down ==="
    echo "Simulating: systemctl stop mysql"
    echo "Expected: registration/login fail, gateway may still serve from Redis"
    echo ""
    systemctl stop mysql
    sleep 1
    echo "Login attempt while MySQL is down..."
    status=$(curl -s -o /dev/null -w '%{http_code}' $BASE/api/login -X POST -H "Content-Type: application/json" -d '{"username":"testuser","password":"testpass123"}')
    echo "Status: $status"
    echo "Restarting MySQL..."
    systemctl start mysql
    sleep 3
    echo "Recovery..."
    status=$(curl -s -o /dev/null -w '%{http_code}' $BASE/api/login -X POST -H "Content-Type: application/json" -d '{"username":"testuser","password":"testpass123"}')
    echo "Status: $status"
    ;;

  "concurrent-balance")
    echo "=== Scenario: Concurrent Balance Check ==="
    echo "Setting balance to exactly enough for 1 request"
    redis-cli SET balance:2 20
    echo "Sending 5 concurrent requests..."
    for i in 1 2 3 4 5; do
      curl -s -o /dev/null -w '%{http_code}\n' $BASE/v1/chat/completions -X POST -H "Content-Type: application/json" -H "X-API-Key: $API_KEY" -d '{"model":"deepseek-chat","messages":[{"role":"user","content":"test"}]}' &
    done
    wait
    echo "Final balance: $(redis-cli GET balance:2)"
    echo "Expected: some succeed, some get 402, balance never negative"
    ;;

  "rate-limit")
    echo "=== Scenario: Rate Limiting ==="
    echo "Sending 120 requests rapidly (limit: 100/min)..."
    for i in $(seq 1 120); do
      curl -s -o /dev/null -w '%{http_code}\n' $BASE/health &
    done
    wait | sort | uniq -c
    echo "Expected: ~100 get 200, rest get 429"
    ;;

  *)
    echo "Usage: bash fault-injection.sh <scenario>"
    echo "Scenarios: upstream-timeout, redis-down, mysql-down, concurrent-balance, rate-limit"
    ;;
esac
