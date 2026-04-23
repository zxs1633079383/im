# syntax=docker/dockerfile:1.7

# ---------- stage 1: build ----------
FROM golang:1.26-alpine AS build

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src

# Cache module downloads first.
COPY server/go.mod server/go.sum ./server/
WORKDIR /src/server
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# Then copy the rest of the server tree.
COPY server/ ./

# Static, stripped binary; no CGO so the distroless static image works.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${GIT_COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/gateway \
      ./cmd/gateway

# ---------- stage 2: runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="im-gateway" \
      org.opencontainers.image.source="https://github.com/cses/im" \
      org.opencontainers.image.description="IM WebSocket gateway (V4 cluster)"

ARG VERSION=dev
ENV IM_VERSION=${VERSION}

# Copy binary + default config template. Operator overrides with a ConfigMap
# mounted at /etc/im/config.yaml.
COPY --from=build /out/gateway /gateway
COPY server/config.example.yaml /etc/im/config.example.yaml

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/gateway"]
