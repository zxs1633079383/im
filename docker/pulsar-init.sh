#!/bin/bash
# Start Pulsar standalone, wait for it to be healthy, then create the
# tenant + namespace that im-server expects. Runs as the container entrypoint.
set -e

echo "[pulsar-init] Starting Pulsar standalone..."
bin/pulsar standalone &
PULSAR_PID=$!

echo "[pulsar-init] Waiting for Pulsar to be ready..."
until curl -s http://localhost:8080/admin/v2/brokers/health >/dev/null 2>&1; do
    sleep 1
done
echo "[pulsar-init] Pulsar is ready."

echo "[pulsar-init] Creating tenant 'im'..."
bin/pulsar-admin tenants create im 2>/dev/null || echo "[pulsar-init] tenant 'im' already exists"

echo "[pulsar-init] Creating namespace 'im/push-local'..."
bin/pulsar-admin namespaces create im/push-local 2>/dev/null || echo "[pulsar-init] namespace 'im/push-local' already exists"

echo "[pulsar-init] Done. Pulsar running (pid=$PULSAR_PID)."
wait $PULSAR_PID
