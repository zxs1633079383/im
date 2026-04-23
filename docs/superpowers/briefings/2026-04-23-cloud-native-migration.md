# Cloud-Native Migration — Wrap-up Briefing

**Branch:** `feature/cloud-native-migration` → main
**Plan:** `docs/superpowers/plans/2026-04-23-cloud-native-migration.md`
**Status:** Complete
**Duration:** ~1 day (single subagent-driven session)

## Outcome

Migrated im-server from a hand-rolled net/http + pgx + raw SQL stack to:

- **Web:** Gin v1.10 + otelgin
- **ORM:** GORM v2 + gorm.io/plugin/opentelemetry/tracing
- **Tests:** testify + mockery + testcontainers-go + httpexpect + gotestsum
- **Quality:** golangci-lint + govulncheck + GitHub Actions CI
- **Observability:** OpenTelemetry (Jaeger trace + Prometheus metrics) end-to-end including
  HTTP requests (otelgin), DB queries (gorm plugin), WebSocket frames (manual spans),
  and Pulsar produce/consume with cross-service trace context propagation.

## Phase summary

| Phase | Description | Outcome |
|---|---|---|
| 0 | CI quality gates | golangci-lint + gotestsum + govulncheck + GitHub Actions |
| 1 | Dependencies + Compose | Gin/GORM/testify/testcontainers/OTel + Jaeger compose |
| 2 | OTel SDK | observability package + trace-aware slog + binary wiring |
| 3 | Test infrastructure | testcontainers helpers (PG/Redis/Pulsar) + httpexpect helper |
| 4 | Gin coexistence shell | Gin engine wrapping legacy mux (strangler-fig) |
| 5 | pgx → GORM full migration | 8 repos + handlers refactored + store/ deleted + pgx removed |
| 6 | Auth Gin slice | First HTTP cutover, established the slice template |
| 7 | 7 remaining slices | profile/settings, friends, channels, messages, sync, search, files, favorites |
| 8 | WS + Pulsar OTel | Cross-service trace propagation + WS metrics gauge |
| 9 | Cleanup + docs | Remove legacy paths, tighten lint, update README |

## Commit count

~50 commits on `feature/cloud-native-migration`. Each Phase 6+ slice produced 1-2 commits (service + handler + integration test + cutover all in one logical commit per the slice template).

## Key adaptations

- pgx v0.18 forced upward bump of testcontainers (0.34→0.35), otel (1.32→1.40 due to CVE), contrib (0.57→0.60)
- Go 1.26.1 → 1.26.2 to fix 4 stdlib CVEs surfaced by govulncheck
- golangci-lint-action switched from binary install to goinstall (binary built with go 1.24, our config targets 1.26)
- mockery v2.50.0 broken on Go 1.26 — bumped to v2.53+ in dev install path
- testcontainers helpers gated behind `//go:build integration` to keep docker/docker CVE out of normal lint scope
- GORM `default:true` tag rewriting in-memory state after Create — handler re-fetches after Upsert to return correct state
- WS NoRoute fallthrough required manual `WriteHeader(200)` to override Gin's pre-set 404 cache

## Test count

- Unit: 230 (was ~120)
- Integration: 309 (was 0 in Go; 1 shell script `sync_test.sh`)

## Lessons

- The strangler-fig pattern (Gin engine wrapping the legacy mux via NoRoute) made cutover painless.
  Each slice landed without breaking anything else, and the team could ship/test each slice independently.
- testcontainers-go is the right call. Real Postgres / Redis / Pulsar in CI catches behaviors that mocks miss
  (e.g., the GORM `default:` tag gotcha was only caught by an integration test).
- OTel SDK + GORM + otelgin "just works" — the trace tree assembles itself with one-line wiring per layer.
  Pulsar requires manual TextMapPropagator inject/extract, but it's mechanical.
- mockery + testify is a fast, low-ceremony combo. EXPECT() API made tests readable.

## Follow-ups (out of scope, tracked elsewhere)

- Drop `default:` tags on bool/string columns in repo models (let DB defaults handle inserts)
- Phase out remaining `golangci-lint` exempts on `internal/gateway/` and `internal/pulsar/` once their
  warnings are addressed
- Bump `gotest.tools/gotestsum` and `mockery` version pins to match what's actually installed in CI
- Wire metrics into Grafana dashboards (collector → prometheus → grafana datasource)
- Consider sqlc for the FetchForUser raw-SQL paths now that the rest is GORM
