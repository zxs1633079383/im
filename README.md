# IM - Instant Messaging System

A full-featured instant messaging system with reliable message delivery, supporting private chats (DM) and group channels with directed-visibility messages.

## Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go 1.26+ |
| Web framework | Gin v1.10 (otelgin) |
| ORM | GORM v2 + `gorm.io/plugin/opentelemetry/tracing` |
| Tests | testify + mockery + testcontainers-go + httpexpect + gotestsum |
| Quality | golangci-lint + govulncheck (GitHub Actions CI) |
| Observability | OpenTelemetry — Jaeger trace + Prometheus metrics |
| Frontend | Tauri v2, Angular 17+, TypeScript |
| Database | PostgreSQL 16 (via GORM) |
| Cache / Routing | Redis 7 (`go-redis`) |
| Message Queue | Apache Pulsar 3 (`apache/pulsar-client-go`) |
| Client Storage | SQLite (via Tauri plugin) |

## Architecture

```
┌─────────────────────────────────────────────┐
│           Client (Tauri + Angular)           │
│  WebSocket + HTTP │ SQLite local storage     │
└───────────────────┬─────────────────────────┘
                    │
┌───────────────────▼─────────────────────────┐
│          Gateway (Go, multi-pod)             │
│  HTTP API + WebSocket + Heartbeat + Push     │
└──────┬────────────────────────┬─────────────┘
       │ Pulsar                 │ Pulsar
       ▼                        ▼
┌──────────────┐    ┌──────────────────────────┐
│ MessageService│    │     Sync (in Gateway)    │
│ Pulsar consumer│   │  Batch sync, read sync   │
└──────┬───────┘    └──────────────────────────┘
       │
       ▼
┌─────────────────────────────────────────────┐
│   PostgreSQL │ Redis │ Pulsar │ (ES via CDC) │
└─────────────────────────────────────────────┘
```

### Message Reliability - Three-Layer Guarantee

1. **Write guarantee** - PG transaction with atomic seq allocation + `client_msg_id` idempotent dedup
2. **Push guarantee** - WebSocket push with client ACK, 3s timeout + 1 retry
3. **Pull guarantee** - Heartbeat pong with seq diff, batch sync on reconnect, count-based hole detection on scroll

### Directed-Visibility Messages (Phantom)

Group messages can be sent to specific members. Non-visible members receive a `phantom` placeholder that occupies the seq position but contains no content. This keeps seq continuous for hole detection while hiding message content.

## Project Structure

```
im/
├── server/                          # Go backend
│   ├── cmd/
│   │   ├── gateway/main.go          # HTTP + WebSocket gateway
│   │   ├── message/main.go          # Pulsar message consumer
│   │   └── sync/main.go             # Sync service (reserved)
│   ├── internal/
│   │   ├── auth/                    # bcrypt + JWT
│   │   ├── config/                  # YAML + env config
│   │   ├── deps/                    # Composition root (DI wiring)
│   │   ├── gateway/                 # WebSocket hub, conn, heartbeat, push consumer
│   │   ├── http/                    # Gin engine, route registration, slice handlers
│   │   ├── middleware/              # JWT, request-id, otelgin
│   │   ├── observability/           # OTel SDK setup (trace + metrics)
│   │   ├── pulsar/                  # Pulsar client wrapper (with OTel propagation)
│   │   ├── repo/                    # GORM repositories (and mockery mocks)
│   │   ├── service/                 # Application services (slice business logic)
│   │   ├── testutil/                # Unit-test helpers
│   │   │   └── containers/          # testcontainers-go helpers (PG/Redis/Pulsar)
│   │   └── ...
│   ├── migrations/                  # PG schema migrations
│   ├── config.example.yaml
│   ├── Makefile
│   └── go.mod
│
├── client/                          # Tauri + Angular desktop app
│   ├── src/app/
│   │   ├── core/                    # Services (auth, channels, messages, friends, ws, db, search, files, favorites)
│   │   ├── features/               # Pages (chat, channel-list, contacts, login, register, search, favorites, profile, settings, etc.)
│   │   └── shared/                  # Guards, interceptors
│   ├── src-tauri/                   # Tauri Rust backend + SQLite plugin
│   ├── angular.json
│   └── package.json
│
└── docs/
    └── superpowers/
        ├── specs/                   # Design spec
        ├── plans/                   # Implementation plans (12)
        └── briefings/               # Execution briefings
```

## Prerequisites

- **Go** 1.26+
- **Node.js** 18+ and npm
- **Rust** (for Tauri build)
- **Docker** (for `make test-integration` via testcontainers-go and the local compose stack)
- **PostgreSQL** 16
- **Redis** 7
- **Apache Pulsar** 3
- **golang-migrate** CLI (`go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest`)

## Setup

### 1. Database

```bash
# Create database
createdb im

# Run migrations
cd server
DATABASE_URL="postgres://your_user@localhost:5432/im?sslmode=disable" make migrate-up
```

### 2. Server Configuration

```bash
cd server
cp config.example.yaml config.yaml
# Edit config.yaml with your PG/Redis/Pulsar connection details
# IMPORTANT: Change jwt_secret in production
```

Environment variable overrides:

| Variable | Description |
|---|---|
| `IM_PG_DSN` | PostgreSQL connection string |
| `IM_REDIS_ADDR` | Redis address |
| `IM_PULSAR_URL` | Pulsar broker URL |
| `IM_JWT_SECRET` | JWT signing secret |
| `IM_GATEWAY_HTTP_ADDR` | Gateway listen address (default `:8080`) |
| `IM_CONFIG` | Config file path (default `config.yaml`) |

### 3. Build & Run Server

```bash
cd server

# Build all services
make build-all

# Run gateway (HTTP + WebSocket)
./bin/gateway

# Run message service (Pulsar consumer, in a separate terminal)
./bin/message
```

### 4. Build & Run Client

```bash
cd client

# Install dependencies
npm install

# Development (Angular dev server)
npm run start
# Then open http://localhost:4200

# Build Tauri desktop app
npx tauri build
```

## Running Tests

```bash
cd server

make test-unit          # fast unit tests (no docker)
make test-integration   # full integration tests (requires Docker — testcontainers boots PG/Redis/Pulsar)
make lint               # golangci-lint
make vuln               # govulncheck
make check              # lint + vuln + test-unit
make check-full         # all of the above + integration
make mocks              # regenerate mockery mocks

# Client build verification
cd client && npx ng build
```

## Observability

The repo ships a docker-compose stack for the full observability pipeline:

```bash
docker compose up -d   # postgres + redis + pulsar + otel-collector + jaeger
```

- **Jaeger UI** — http://localhost:16686 (HTTP requests, GORM queries, WebSocket frames, and Pulsar produce/consume spans, with cross-service trace context propagation)
- **Prometheus scrape** — http://localhost:8889/metrics (process metrics, Gin RED, GORM stats, WebSocket gauges)
- Set `OTEL_DISABLED=true` to disable OTel exporters (handy for local dev without the collector running)

## API Overview

| Category | Endpoints |
|---|---|
| Auth | `POST /api/auth/register`, `POST /api/auth/login`, `GET /api/auth/me` |
| Channels | `POST /api/channels`, `POST /api/channels/dm`, `GET /api/channels`, `GET/PUT /api/channels/{id}`, member management, leave |
| Messages | `POST /api/channels/{id}/messages`, `GET /api/channels/{id}/messages`, `POST /api/channels/{id}/read`, `POST /api/messages/forward` |
| Sync | `POST /api/sync` (batch sync) |
| Friends | `POST /api/friends/request`, accept, reject, `GET /api/friends`, pending, block |
| Search | `GET /api/search?q=&type=messages\|users\|channels` |
| Files | `POST /api/files` (upload), `GET /api/files/{id}` (download) |
| Favorites | `POST/DELETE /api/favorites/{message_id}`, `GET /api/favorites` |
| Profile | `PUT /api/users/me`, `GET/PUT /api/settings` |
| WebSocket | `GET /ws?token=xxx&device=yyy` |

All endpoints except auth require JWT Bearer token. See `docs/superpowers/briefings/2026-04-02-final-briefing.md` for the complete API reference.
