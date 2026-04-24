#!/usr/bin/env bash
# e2e-teardown.sh — scrub the pre environment so repeat runs start clean.
#
# Assumes:
#   - A port-forward is open to pre PG at localhost:25432 (see kubectl
#     -n postgres-cses port-forward svc/postgresql-cses-pre-cnpg-rw 25432:5432).
#   - Your local kubectl context points at pre cluster.
#
# Clears:
#   1. im_pre PostgreSQL — TRUNCATE all im tables, reset sequences.
#   2. Redis Cluster — DEL every key matching im-new:* across all masters.
#   3. Pulsar im/push-pre namespace — delete every topic (recreated on demand).
#
# Does NOT drop the im_pre database, the redis cluster, or the Pulsar
# namespace / tenant — these are single-provision resources.

set -euo pipefail

PGPASSWORD="${PGPASSWORD:-one.2013}"
PG_HOST="${PG_HOST:-localhost}"
PG_PORT="${PG_PORT:-25432}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-im_pre}"

echo "==> TRUNCATE im_pre"
# Dynamic approach: truncate every non-migration-bookkeeping table so new
# migrations don't need this script updated.
PGPASSWORD="$PGPASSWORD" psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" <<'SQL'
DO $$
DECLARE t text;
BEGIN
    FOR t IN
        SELECT tablename FROM pg_tables
        WHERE schemaname='public' AND tablename <> 'schema_migrations'
    LOOP
        EXECUTE 'TRUNCATE ' || quote_ident(t) || ' RESTART IDENTITY CASCADE';
    END LOOP;
END $$;
SQL

echo "==> DEL im-new:* from Redis Cluster"
# Pick one Redis pod to drive the cluster call. cluster-call fans out to all
# masters so we only need one entry point.
REDIS_POD=$(kubectl -n redis-cses get pods -l app=redis-cluster -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$REDIS_POD" ]]; then
    REDIS_POD=$(kubectl -n redis-cses get pods -o jsonpath='{.items[0].metadata.name}')
fi
kubectl -n redis-cses exec "$REDIS_POD" -- sh -c '
for host in $(redis-cli cluster nodes | grep master | awk "{print \$2}" | cut -d@ -f1); do
    redis-cli -h ${host%:*} -p ${host##*:} --scan --pattern "im-new:*" | while read -r k; do
        redis-cli -h ${host%:*} -p ${host##*:} DEL "$k" > /dev/null
    done
done
' || echo "   (redis scrub best-effort; ignoring errors)"

echo "==> delete Pulsar topics under im/push-pre"
kubectl -n pulsar-cses exec pulsar-cses-toolset-0 -- bin/pulsar-admin topics list persistent://im/push-pre 2>/dev/null \
    | tr -d '\r' \
    | while read -r topic; do
        [[ -z "$topic" ]] && continue
        kubectl -n pulsar-cses exec pulsar-cses-toolset-0 -- bin/pulsar-admin topics delete --force "$topic" || true
    done

echo "==> teardown done"
