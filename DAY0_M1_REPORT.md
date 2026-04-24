# DAY 0 + M1 HTTP Wrap-Up Report

Worktree: `/Users/mac28/workspace/golangProject/im/.claude/worktrees/agent-a1cefd93`
Branch: `worktree-agent-a1cefd93`

## Inherited Prior Work

- `4664756` feat(m1): DAY 0 A1 + A2 + M1 schema prep — A1 `/api/sync` contract,
  A2 `AllocSeqAndInsert`, M1 schema migration (added `updated_at`, `deleted`,
  `deleted_at` on `messages` + repo surface for edit/delete/replies/readers/
  around-timestamp).

## New Commits (this session, in order)

```
05404a4 feat(gateway): producer cache per topic
c5e63c5 feat(gateway): env-aware push topic naming
4017c37 feat(routing): 45s TTL refresh on ping
9b5c703 feat(gateway): CrossPodPush with producer cache + routing
cb52bf1 feat(gateway): refresh routing on ping
c8d9ce1 feat(gateway): cross-pod push wiring in main + 4 pushers
dacaf13 feat(message): DELETE /messages/:id soft-delete + msg_deleted broadcast
5eda4b8 feat(message): PATCH /messages/:id edit + msg_updated broadcast
2d59679 feat(message): GET /messages/:id/replies thread listing
ee3c017 feat(message): GET /messages/:id/readers with cursor pagination
ca2c10b feat(message): GET /channels/:id/messages/around timestamp-based fetch
6892ef8 chore(build): Makefile verify-all target for V1+V2
```

Note: The `TypeMsgUpdated` / `TypeMsgDeleted` WS constants were added as part of
`9b5c703` (the CrossPodPush commit) because the unit tests for CrossPodPush
reference them directly. Effectively the "Phase W commit" was folded into A3.4
to keep each commit independently compile-green.

## What Landed

### Phase A3 — Cross-Pod Push (complete)

- `internal/gateway/producer_cache.go` — LRU(256) topic → `*imPulsar.Producer`,
  onEvict closes producers, `sync.Mutex`-guarded miss-fill to avoid duplicate
  opens per topic. `go.mod` gains `github.com/hashicorp/golang-lru/v2`.
- `internal/gateway/topic.go` + `topic_test.go` — `PushTopicFor(gwID, env)`
  returns `persistent://im/push/...` (prod), `persistent://im/push-pre/...`
  (pre), or `persistent://im/push-local/...{$USER|$HOSTNAME|anon}` (other).
  Test covers all four env buckets + both fallback paths.
- `internal/repo/routing.go` — `RoutingTTL = 45s` and `Refresh(ctx, uid, gwID,
  connID)` atomic HSET+EXPIRE Lua, plus a `Lookup` alias for the
  BACKEND.md naming. Gateway side re-exports `RoutingTTL` via `routing.go`.
- `internal/gateway/cross_pod_push.go` — `Hub.CrossPodPush(...)` with narrow
  `routingLookup` / `producerGetter` / `crossPodSender` interfaces so the core
  loop is testable without Redis / Pulsar. Implements: local-hit
  short-circuit → routing lookup → per-target producer fan-out (skip self,
  per-user partition key = userID) → all-offline log.
- `internal/gateway/cross_pod_push_test.go` — 5 unit tests: LocalHit,
  RemoteOnly, AllOffline, RoutingError, SkipsSelf. All pass with `-race`.
- `internal/gateway/ws_handler.go` — ping handler now fires an async
  `routing.Refresh` goroutine with a 2s timeout, so Redis failures never block
  the read pump.
- `cmd/gateway/main.go` — builds a shared `crossPodDeps{hub,routing,cache,
  gatewayID,env,log}` value; every HTTP-side pusher (friend / channel /
  message / read-sync) is refactored to route through `CrossPodPush`.
  `IM_ENV` drives the topic namespace (default "local"). Added
  `hubEventBroadcaster` implementing the new `MessageEventBroadcaster`
  interface for msg_updated / msg_deleted fan-out.

### Phase W — WS Message Types (complete)

- `internal/gateway/types.go` adds `TypeMsgUpdated = "msg_updated"` and
  `TypeMsgDeleted = "msg_deleted"`. Committed inline with A3.4.

### Phase B1–B5 — M1 HTTP Endpoints (complete)

The repo layer (`UpdateContent`, `SoftDelete`, `FetchReplies`, `GetReaders`,
`FetchAroundTimestamp`) was already built by the prior agent in the M1 schema
commit. This session wrote the service + HTTP layers and wired broadcasting.

| Endpoint | Method | Handler | Service method |
|---|---|---|---|
| `/api/messages/:id` | DELETE | B1 | `MessageService.DeleteMessage` |
| `/api/messages/:id` | PATCH | B2 | `MessageService.EditMessage` |
| `/api/messages/:id/replies` | GET | B3 | `MessageService.GetReplies` |
| `/api/messages/:id/readers` | GET (cursor, limit) | B4 | `MessageService.GetReaders` |
| `/api/channels/:id/messages/around?timestamp=` | GET | B5 | `MessageService.FetchAroundTimestamp` |

Error mapping (HTTP):
- `repo.ErrNotFound` → 404
- `repo.ErrForbidden` → 403 ("not the message sender")
- `repo.ErrGone` (edit) → 410; (delete) → 200 idempotent ok, `already_deleted: true`
- `service.ErrNotMember` → 403

B1/B2 broadcast to every channel member via the new
`MessageEventBroadcaster` interface added to `MessageRouteOpts`. The interface
uses a string-typed `MessageEventType` so `internal/http` stays free of a
`gateway` import. `cmd/gateway/main.go` converts back to
`gateway.WSMessageType` inside `hubEventBroadcaster.BroadcastToMembers`.

B4 uses a cursor on `user_id` (response: `{readers: [], next_cursor: N}`).
B5 accepts `?timestamp=<ms>&limit=<N>` (defaults: limit=50, clamp 1..100);
response: `{messages: [], has_older: bool, has_newer: bool}` with `messages`
already sorted by seq ASC.

### Phase M — Makefile (complete)

`server/Makefile` gains `verify-all`, `verify-build`, `verify-unit`,
`verify-integration` targets matching OVERALL.md §5.1. `verify-build` includes
`go vet` and optional `golangci-lint` (skipped if not installed).
`verify-unit` is `go test -race -short ./...`. `verify-integration` is opt-in.

## Final Build + Test

```
$ go build ./...          # → clean (no output)
$ go test -race -short ./...
?   	im-server/cmd/gateway	[no test files]
?   	im-server/cmd/message	[no test files]
?   	im-server/cmd/sync	[no test files]
ok  	im-server/internal/auth		1.054s (cached)
ok  	im-server/internal/config	(cached)
ok  	im-server/internal/gateway	1.054s
ok  	im-server/internal/http		1.765s
ok  	im-server/internal/middleware	(cached)
ok  	im-server/internal/observability (cached)
?   	im-server/internal/pulsar	[no test files]
?   	im-server/internal/repo		[no test files]
?   	im-server/internal/repo/mocks	[no test files]
ok  	im-server/internal/service	1.625s
?   	im-server/internal/testutil	[no test files]
```

`golangci-lint run` also clean.

## Remaining BLOCKERs / Follow-Ups

1. **Integration tests not run.** Instruction explicitly forbade
   `go test -tags=integration` / `docker-compose up` in this session. Next
   human-in-the-loop step: run
   `cd server && make verify-integration` (pg+redis+pulsar via
   `docker-compose.yml`) and confirm M1 endpoints round-trip.

2. **No service-level unit tests for new HTTP endpoints.** The repo layer
   (UpdateContent/SoftDelete/FetchReplies/GetReaders/FetchAroundTimestamp)
   already has behavior baked in via previous agent's schema prep; the new
   Service methods (`DeleteMessage`, `EditMessage`, `GetReplies`,
   `GetReaders`, `FetchAroundTimestamp`) are thin wrappers that mostly
   delegate + do membership checks. They compile but have no dedicated
   tests in this session — covered transitively by `internal/http` handler
   tests in next iteration. Recommend adding table-driven tests in
   `server/internal/service/message_test.go` (existing file) before M1 ship.

3. **Unit test for `Routing.Refresh` against miniredis.** The 45s TTL Lua
   path is only exercised under integration. Adding a miniredis-backed
   unit test in `internal/repo` would pin down the happy path without
   requiring docker.

4. **`ReadSyncer` now uses `CrossPodPush`**, so read_sync to a user
   attached to another pod is only best-effort until M2 lands its offline
   message store. In the current single-pod default deployment this is a
   no-op risk.

5. **`hubEventBroadcaster` enumerates members synchronously before
   fanout.** For very large channels that could slow the request handler's
   defer chain (the broadcast runs inside the handler response path via
   `BroadcastToMembers`). Current handlers call it AFTER `c.JSON(...)` so
   this is fine today, but future edits may regress.

## Suggested Next Steps

1. Pre-merge: reviewer runs `make verify-integration` locally with
   docker-compose; triage any Pulsar topic-naming mismatch vs the
   `PushTopicFor` convention if tests hit real topics.
2. Add service-layer unit tests for M1 new methods (DeleteMessage /
   EditMessage / GetReplies / GetReaders / FetchAroundTimestamp) using
   the existing `MessageRepoMock` — all mocks already have matching
   signatures.
3. M2 handoff: design an offline message store so `CrossPodPush`'s
   `"offline fanout deferred"` log grows teeth (durable queue keyed on
   userID).
4. Run `make verify-integration` in CI against a per-branch docker-compose
   and gate merges on it before M1 ships to staging.
