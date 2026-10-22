#!/usr/bin/env bash
set -euo pipefail

for tool in docker floci aws go curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'Missing required tool: %s\n' "$tool" >&2
    exit 1
  fi
done

eval "$(floci env --host localhost)"
set -a
source .env
set +a
export AWS_PAGER=""

docker compose up -d floci

ready=false
for attempt in {1..60}; do
  if curl -fsS "$AWS_ENDPOINT_URL/_localstack/health" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done

if [[ "$ready" != true ]]; then
  printf 'Floci did not become ready. Check: docker compose logs floci\n' >&2
  exit 1
fi

go run scripts/provision.go
docker compose up -d --build --wait app
printf '\nOpen http://localhost:%s\n' "$BACKEND_PORT"
