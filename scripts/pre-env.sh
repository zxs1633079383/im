#!/usr/bin/env bash
# pre-env.sh — source this before running scripts/v4-prepare.sh or
# scripts/v4-cluster-test.sh against the pre cluster (im-v2 namespace).
#
# Usage:
#   source scripts/pre-env.sh
#   scripts/v4-prepare.sh
#
# All values are derived from the pre-cluster infra (postgres-cses / redis-cses
# / pulsar-cses) and match the k8s SVC DNS names. JWT_SECRET is generated per
# run with openssl if not already set — treat it as ephemeral.

export NAMESPACE="im-v2"
export REGISTRY="harbor.jinqidongli.com/x9-go/im"
export IMAGE_TAG="${IMAGE_TAG:-v1.0.0-pre}"

# PostgreSQL — shared cnpg cluster, im gets its own database im_pre.
# Admin must pre-create the DB:
#   psql -h postgresql-cses-pre-cnpg-rw.postgres-cses -U postgres \
#        -c 'CREATE DATABASE im_pre;'
export PG_DSN="postgres://postgres:one.2013@postgresql-cses-pre-cnpg-rw.postgres-cses.svc.cluster.local:5432/im_pre?sslmode=disable"

# Redis — shared Cluster. Isolation via key prefix im-new:* (see
# repo/routing.go). Headless DNS resolves to one A record; Cluster mode
# auto-discovers the remaining master/slave nodes.
export REDIS_ADDR="redis-cses-pre-redis-cluster-headless.redis-cses.svc.cluster.local:6379"
export REDIS_CLUSTER="true"

# Pulsar — im has its own tenant + namespace (im/push-pre) for isolation.
# Broker (headless) is the in-cluster address; proxy (NodePort 32650) is the
# out-of-cluster fallback.
export PULSAR_URL="pulsar://pulsar-cses-broker.pulsar-cses.svc.cluster.local:6650"

# Ephemeral JWT secret — regenerated per run unless pre-set. Do not commit a
# static one, and do not share across environments.
if [[ -z "${JWT_SECRET:-}" ]]; then
    export JWT_SECRET="$(openssl rand -hex 32)"
    echo "pre-env: generated ephemeral JWT_SECRET"
fi

cat <<SUMMARY
pre-env loaded:
  NAMESPACE      = $NAMESPACE
  REGISTRY       = $REGISTRY
  IMAGE_TAG      = $IMAGE_TAG
  PG_DSN         = $PG_DSN
  REDIS_ADDR     = $REDIS_ADDR
  REDIS_CLUSTER  = $REDIS_CLUSTER
  PULSAR_URL     = $PULSAR_URL
  JWT_SECRET     = (hidden, ${#JWT_SECRET} chars)
SUMMARY
