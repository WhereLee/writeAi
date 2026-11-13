# LLM API Gateway

![CI](https://github.com/WhereLee/writeAi/actions/workflows/ci.yml/badge.svg)
![CD](https://github.com/WhereLee/writeAi/actions/workflows/cd.yml/badge.svg)
![Uptime](https://github.com/WhereLee/writeAi/actions/workflows/uptime.yml/badge.svg)

LLM API billing gateway: admins grant credits, users call LLM APIs through a unified endpoint, and are billed per actual token consumption.

## Architecture

- **Go backend**: API gateway, billing engine, rate limiter
- **React frontend**: User console (credits, API keys, ledger, usage) and admin console
- **MySQL**: Users, balances, credit operations, balance logs, usage logs
- **Redis**: Rate limiting

## Local Development

```bash
# Backend
cd backend
# Windows PowerShell: bind loopback to avoid a firewall prompt
$env:BIND_ADDR="127.0.0.1"; go run ./cmd/server

# Frontend
cd frontend
npm install && npm run dev

# Load test
cd k6
BASE_URL=http://localhost:8080 API_KEY=sk-xxx MODEL=deepseek-chat k6 run loadtest.js
```

Frontend API address is read from `VITE_API_BASE`:

```bash
cp frontend/.env.example frontend/.env
```

## API

- `POST /api/register`, `POST /api/login`
- `GET /api/models` (public)
- `GET /api/me`, `GET /api/balance`, `GET /api/keys`, `POST /api/keys`, `DELETE /api/keys/{id}` (JWT)
- `GET /api/credits/ledger`, `GET /api/usage` (JWT)
- `POST /api/admin/credits/grant`, `POST /api/admin/credits/revoke` (admin)
- `GET /api/admin/users`, `GET /api/admin/credits/operations`, `GET /api/admin/audit-logs` (admin)
- `POST /v1/chat/completions` (`X-API-Key`)
- `GET /healthz`, `GET /readyz`, `GET /metrics`

Credits are the display unit of the token balance. `1 credit = TOKENS_PER_CREDIT`
tokens (default 1000). All ledger entries are stored as integer tokens. Credits
cannot be withdrawn, transferred, or exchanged for cash.

Grant and revoke requests accept an `Idempotency-Key` header. Repeating the same
key from the same admin returns the original operation without changing the
balance again. Every admin action writes an audit log row in the same
transaction.

The first admin is promoted by setting `BOOTSTRAP_ADMIN_USERNAMES` to an existing
username, or by running:

```sql
UPDATE users SET role = 'admin' WHERE username = '<name>';
```

`/v1/chat/completions` supports `stream: true` for Server-Sent Events.
API keys are stored as SHA-256 hashes; the full key is returned only once.
Registration requires `REGISTRATION_INVITE_CODE` when that variable is set.

`CORS_ORIGINS` is a comma-separated allowlist. Set it to the frontend origin,
for example `https://118.31.111.223:8080`.

## Deploy

Production runs the gateway as a single Docker container defined in
`deploy/docker-compose.server.yml`, using host networking so it can reach the
host MySQL and Redis instances.

- `CI` runs on pushes and pull requests to `main`.
- `CD` runs after `CI` succeeds on `main`: it builds and pushes
  `ghcr.io/wherelee/llm-api-gateway`, then pulls and restarts the container on
  the server with a health check and rollback.
- Server deployment logic lives in `scripts/deploy.sh`: pull retries, lock,
  health check and rollback.
- Production entrypoint is Nginx on `8080` with a self-signed certificate: it serves `/opt/dist` and proxies
  `/api/`, `/v1/`, `/healthz` and `/readyz` to `127.0.0.1:18080`. Use `https://118.31.111.223:8080`; browsers will warn until you trust `llm-gateway.crt`, or use `curl -k`.
- Domain, ICP filing and payment are intentionally out of scope here. They live in the
  companion course-platform project. This gateway keeps a self-signed IP entrypoint for
  internal use and demos, so the two projects do not duplicate the same business surface.
- `scripts/backup.sh` runs daily from `/etc/cron.d/llm-gateway-backup`.
- Prometheus alert rules are in `prometheus-alerts.yml`.
- Manual deploys: Actions -> CD -> Run workflow.
- Required repository secrets: `SERVER_HOST`, `SERVER_USER`, `SERVER_SSH_KEY`.
- SSH password authentication is disabled; CD uses `SERVER_SSH_KEY` only.
- First-time server setup: copy `deploy/server.env.example` to
  `/opt/llm-gateway/server.env`, fill in real values, and `chmod 600` it.

## Testing

```bash
cd backend
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1

cd ../frontend
npm run build

cd ..
actionlint .github/workflows/ci.yml .github/workflows/cd.yml
```
