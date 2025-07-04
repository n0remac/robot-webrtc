#!/usr/bin/env bash
set -euo pipefail

# Change these if you need a different range
MIN_PIN=0
MAX_PIN=15

for pin in $(seq $MIN_PIN $MAX_PIN); do
  for dir in 1 -1; do
    echo "→ Testing pin $pin, direction $dir"
    sleep 1
    go run ./cmd/testclient \
      -pin="$pin" \
      -direction="$dir" \
      -speed=60 \
      -duration=2s
  done
done

echo "✅ All pins tested."
