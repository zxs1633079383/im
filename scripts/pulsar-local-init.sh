#!/usr/bin/env bash
# pulsar-local-init.sh — one-shot init for the local standalone Pulsar
# container started by docker-compose.yml.
#
# Creates the `im` tenant + `im/push-local` namespace that the gateway's
# PushConsumer subscribes to (see internal/gateway/topic.go). Without this,
# gateway startup errors with:
#   TopicNotFound: subscribe to persistent://im/push-local/msg.push.<uuid>.<user>
#
# Safe to re-run — every command uses /dev/null redirection and tolerates
# "already exists" errors.
#
# Usage:
#   docker compose up -d
#   scripts/pulsar-local-init.sh
#   make build-all && ./bin/gateway

set -uo pipefail

CONTAINER="${PULSAR_CONTAINER:-im-pulsar}"

if ! docker ps --format '{{.Names}}' | grep -qx "$CONTAINER"; then
    echo "error: container '$CONTAINER' is not running. Start it with:" >&2
    echo "  docker compose up -d pulsar" >&2
    exit 2
fi

# Wait for Pulsar to accept admin calls (standalone boots ~10-20s).
echo "==> waiting for Pulsar admin to come up..."
for i in $(seq 1 60); do
    if docker exec "$CONTAINER" bin/pulsar-admin clusters list >/dev/null 2>&1; then
        echo "    ready after ${i}s"
        break
    fi
    sleep 1
done

echo "==> ensure tenant 'im'"
docker exec "$CONTAINER" bin/pulsar-admin tenants create im \
    --allowed-clusters standalone 2>&1 | grep -v 'already exists' || true

echo "==> ensure namespace 'im/push-local'"
docker exec "$CONTAINER" bin/pulsar-admin namespaces create im/push-local 2>&1 \
    | grep -v 'already exists' || true

# Short subscription retention so abandoned dev sessions don't pile up.
echo "==> set subscription-expiration-time to 10m"
docker exec "$CONTAINER" bin/pulsar-admin namespaces \
    set-subscription-expiration-time im/push-local --time 10 >/dev/null 2>&1 || true

echo "==> verify"
docker exec "$CONTAINER" bin/pulsar-admin tenants list
echo "---"
docker exec "$CONTAINER" bin/pulsar-admin namespaces list im
echo
echo "local Pulsar ready. Start gateway with:  ./bin/gateway"
