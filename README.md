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
k6 run loadtest.js
```

## Deploy

Production runs the gateway as a single Docker container defined in
`deploy/docker-compose.server.yml`, using host networking so it can reach the
host MySQL and Redis instances.

- `CI` runs on pushes and pull requests to `main`.
- `CD` runs after `CI` succeeds on `main`: it builds and pushes
  `ghcr.io/wherelee/llm-api-gateway`, then pulls and restarts the container on
  the server with a health check and rollback.
- Manual deploys: Actions -> CD -> Run workflow.
- Required repository secrets: `SERVER_HOST`, `SERVER_USER`, `SERVER_PASSWORD`.
- First-time server setup: copy `deploy/server.env.example` to
  `/opt/llm-gateway/server.env`, fill in real values, and `chmod 600` it.
