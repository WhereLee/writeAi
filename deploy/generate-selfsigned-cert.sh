#!/usr/bin/env bash
set -eu

IP="${IP:-118.31.111.223}"
DIR="${DIR:-/etc/nginx/ssl}"
mkdir -p "$DIR"
openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
  -keyout "$DIR/llm-gateway.key" \
  -out "$DIR/llm-gateway.crt" \
  -subj "/CN=$IP" \
  -addext "subjectAltName=IP:$IP,IP:127.0.0.1,DNS:localhost"
chmod 600 "$DIR/llm-gateway.key"
chmod 644 "$DIR/llm-gateway.crt"
echo "generated $DIR/llm-gateway.crt for $IP"