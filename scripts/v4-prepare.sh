#!/usr/bin/env bash
# v4-prepare.sh — build + push the gateway image and render k8s manifests
# for the V4 cluster scenarios (OVERALL.md §5.5).
#
# Usage:
#   NAMESPACE=im-pre \
#   REGISTRY=harbor.internal/im \
#   IMAGE_TAG=v0.1.0-m1-verified \
#   PG_DSN='postgres://im:im@im-pg:5432/im?sslmode=disable' \
#   REDIS_ADDR='im-redis:6379' \
#   PULSAR_URL='pulsar://im-pulsar:6650' \
#   JWT_SECRET='please-rotate-this' \
#   ./scripts/v4-prepare.sh
#
# Optional:
#   SKIP_BUILD=1     skip docker build
#   SKIP_PUSH=1      skip docker push
#   DOCKERFILE=Dockerfile
#   CONTEXT=.        build context

set -euo pipefail

: "${NAMESPACE:?NAMESPACE is required}"
: "${REGISTRY:?REGISTRY is required (e.g. harbor.internal/im)}"
: "${IMAGE_TAG:?IMAGE_TAG is required (e.g. v0.1.0-m1-verified)}"
: "${PG_DSN:?PG_DSN is required}"
: "${REDIS_ADDR:?REDIS_ADDR is required}"
: "${PULSAR_URL:?PULSAR_URL is required}"
: "${JWT_SECRET:?JWT_SECRET is required}"

DOCKERFILE="${DOCKERFILE:-Dockerfile}"
CONTEXT="${CONTEXT:-.}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
K8S_DIR="$REPO_ROOT/deploy/k8s"
OUT_DIR="$K8S_DIR/rendered"

IMAGE="$REGISTRY/im-gateway:$IMAGE_TAG"

echo "==> repo root:    $REPO_ROOT"
echo "==> dockerfile:   $DOCKERFILE"
echo "==> image:        $IMAGE"
echo "==> namespace:    $NAMESPACE"
echo

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  echo "==> docker build"
  docker build \
    --build-arg VERSION="$IMAGE_TAG" \
    --build-arg GIT_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -t "$IMAGE" \
    -f "$REPO_ROOT/$DOCKERFILE" \
    "$REPO_ROOT/$CONTEXT"
else
  echo "==> SKIP_BUILD=1: skipping docker build"
fi

if [[ "${SKIP_PUSH:-0}" != "1" ]]; then
  echo "==> docker push"
  docker push "$IMAGE"
else
  echo "==> SKIP_PUSH=1: skipping docker push"
fi

echo "==> rendering manifests into $OUT_DIR"
mkdir -p "$OUT_DIR"

# Escape replacement values for sed: the delimiter we use is '#' so we must
# escape any literal '#' and '\' characters. newlines aren't expected in
# these values.
esc() { printf '%s' "$1" | sed -e 's/[\\#&]/\\&/g'; }

NS_E=$(esc "$NAMESPACE")
REG_E=$(esc "$REGISTRY")
TAG_E=$(esc "$IMAGE_TAG")
PG_E=$(esc "$PG_DSN")
REDIS_E=$(esc "$REDIS_ADDR")
PULSAR_E=$(esc "$PULSAR_URL")
JWT_E=$(esc "$JWT_SECRET")

shopt -s nullglob
for f in "$K8S_DIR"/*.yaml; do
  base="$(basename "$f")"
  sed \
    -e "s#__NAMESPACE__#$NS_E#g" \
    -e "s#__REGISTRY__#$REG_E#g" \
    -e "s#__IMAGE_TAG__#$TAG_E#g" \
    -e "s#__PG_DSN__#$PG_E#g" \
    -e "s#__REDIS_ADDR__#$REDIS_E#g" \
    -e "s#__PULSAR_URL__#$PULSAR_E#g" \
    -e "s#__JWT_SECRET__#$JWT_E#g" \
    "$f" > "$OUT_DIR/$base"
  echo "   rendered $base"
done

# Also rewrite the deployment image name to the full registry+tag so the
# rendered manifests are self-contained (no kustomize required at apply
# time). The ConfigMap/Secret/Service/HPA/PDB don't reference the image.
if command -v sed >/dev/null 2>&1; then
  sed -i.bak \
    -e "s#image: im-gateway:$TAG_E#image: $REG_E/im-gateway:$TAG_E#g" \
    "$OUT_DIR/20-deployment.yaml"
  rm -f "$OUT_DIR/20-deployment.yaml.bak"
fi

echo
echo "==> done. rendered manifests are in: $OUT_DIR"
echo "==> next:"
echo "    kubectl apply -f $OUT_DIR/"
echo "    kubectl -n $NAMESPACE rollout status deploy/im-gateway"
