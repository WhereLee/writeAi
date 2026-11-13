# LLM API Gateway

LLM API billing gateway: users purchase token credits, call LLM APIs through a unified endpoint, and are billed per actual token consumption.

## Architecture

- **Go backend**: API gateway, billing engine, rate limiter
- **React frontend**: User console (balance, API keys, plans)
- **MySQL**: Users, orders, balance logs, usage logs
- **Redis**: Balance cache, rate limiting, distributed locking

## Local Development

```bash
# Backend
cd backend
go run ./cmd/server

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
- `GET /api/plans`, `GET /api/models` (public)
- `GET /api/balance`, `GET /api/keys`, `POST /api/keys`, `DELETE /api/keys/{id}` (JWT)
- `GET /api/orders`, `POST /api/orders`, `POST /api/orders/{id}/pay` (JWT)
- `POST /v1/chat/completions` (`X-API-Key`)
- `GET /healthz`, `GET /readyz`, `GET /metrics`

Order payment is a demo endpoint. It only works when
`ALLOW_MOCK_PAYMENT=true`; keep it disabled in production.

`/v1/chat/completions` supports `stream: true` for Server-Sent Events.
API keys are stored as SHA-256 hashes; the full key is returned only once.
Registration requires `REGISTRATION_INVITE_CODE` when that variable is set.

`CORS_ORIGINS` is a comma-separated allowlist. Set it to the frontend origin,
for example `http://118.31.111.223:8080`.

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
- Production entrypoint is Nginx on `8080`: it serves `/opt/dist` and proxies
  `/api/`, `/v1/`, `/healthz` and `/readyz` to `127.0.0.1:18080`.
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
