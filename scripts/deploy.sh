#!/usr/bin/env bash
set -eu

APP_DIR="${APP_DIR:-/opt/llm-gateway}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.server.yml}"
IMAGE_REPO="${IMAGE_REPO:-ghcr.io/wherelee/llm-api-gateway}"
IMAGE_TAG="${IMAGE_TAG:?IMAGE_TAG is required}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:8080/healthz}"
PULL_TIMEOUT="${PULL_TIMEOUT:-180}"
PULL_RETRIES="${PULL_RETRIES:-3}"
LOCK_FILE="${LOCK_FILE:-/tmp/llm-gateway-deploy.lock}"

cd "$APP_DIR"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "[deploy] another deploy is already running"
  exit 1
fi

compose="docker compose -f $COMPOSE_FILE"
image="$IMAGE_REPO:$IMAGE_TAG"
prev=$(cat DEPLOYED_SHA 2>/dev/null || true)

log() { echo "[deploy] $*"; }

if docker image inspect "$image" >/dev/null 2>&1; then
  log "image already local: $image"
else
  pulled=0
  for attempt in $(seq 1 "$PULL_RETRIES"); do
    log "pull attempt $attempt/$PULL_RETRIES: $image"
    if timeout -k 10 "$PULL_TIMEOUT" $compose pull server; then
      pulled=1
      break
    fi
    log "pull attempt $attempt failed"
    sleep 5
  done
  if [ "$pulled" != "1" ]; then
    log "pull failed; keeping current container"
    exit 1
  fi
fi

if systemctl is-active --quiet llm-server; then
  log "disabling legacy systemd service"
  sudo -n systemctl disable --now llm-server
fi

if ! $compose up -d server; then
  log "docker compose up failed"
  $compose logs --tail=50 server || true
  if [ -n "$prev" ]; then
    log "rolling back to $prev"
    export IMAGE_TAG="$prev"
    $compose up -d server || true
  else
    $compose down || true
    sudo -n systemctl enable --now llm-server || true
  fi
  exit 1
fi

healthy=0
for _ in $(seq 1 30); do
  if curl -fsS "$HEALTH_URL" >/dev/null; then
    healthy=1
    break
  fi
  sleep 1
done

if [ "$healthy" != "1" ]; then
  log "health check failed"
  $compose logs --tail=50 server || true
  if [ -n "$prev" ]; then
    log "rolling back to $prev"
    export IMAGE_TAG="$prev"
    $compose up -d server || true
  else
    $compose down || true
    sudo -n systemctl enable --now llm-server || true
  fi
  exit 1
fi

echo "$IMAGE_TAG" > DEPLOYED_SHA
$compose ps
log "deployed $IMAGE_TAG"