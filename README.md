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

```bash
docker-compose up -d
```
