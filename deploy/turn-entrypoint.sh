#!/bin/sh
set -eu

secret="$(cat /run/secrets/turn_shared_secret)"
if [ -z "$secret" ]; then
  echo "TURN shared secret is empty" >&2
  exit 1
fi

exec turnserver \
  --listening-port=3478 \
  --min-port=49160 \
  --max-port=49200 \
  --external-ip="$SERVER_PUBLIC_IP" \
  --realm="$TURN_HOST" \
  --server-name="$TURN_HOST" \
  --use-auth-secret \
  --static-auth-secret="$secret" \
  --fingerprint \
  --no-multicast-peers \
  --no-cli \
  --no-tls \
  --no-dtls
