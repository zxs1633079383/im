// v4-load.js — k6 load test for the V4 cluster (OVERALL.md §5.5 S5).
//
// Target: 150,000 concurrent WS clients, 10k msg/s sustained, p99 push
// latency < 80ms. Each VU:
//   1. POST /api/auth/register (or /login if we get 409) to obtain a JWT.
//   2. Open a WebSocket to /ws?token=<jwt>.
//   3. Heartbeat ping every 15s; optionally emit a small fraction of
//      sends into a seeded DM channel.
//
// Usage:
//   API_BASE=http://<gateway-LB>:8080 \
//   WS_BASE=ws://<gateway-LB>:8080 \
//   V4_PASS=v4test1234 \
//   k6 run scripts/v4-load.js
//
// Env knobs:
//   TARGET_VUS        default 150000
//   DM_PEER_ID        peer user ID for outbound sends; if unset, each
//                     VU just receives (pure connection load).
//   SEND_PROB         fraction [0..1] of VUs that send periodically
//                     (default 0.1 — 10% of VUs send).
//   SEND_INTERVAL_MS  default 15000 (one send every 15s per sender)
//
// Note: 150k connections from a single k6 runner is aggressive — plan
// for multiple runner nodes or a distributed run. Tune your OS
// nofile / somaxconn on both the runner and each gateway pod.

import ws from 'k6/ws';
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';

const API_BASE = __ENV.API_BASE || 'http://localhost:8080';
const WS_BASE  = __ENV.WS_BASE  || 'ws://localhost:8080';
const PASSWORD = __ENV.V4_PASS  || 'v4test1234';
const USER_PREFIX = __ENV.V4_USER_PREFIX || 'v4load';
const TARGET_VUS  = parseInt(__ENV.TARGET_VUS || '150000', 10);
const SEND_PROB   = parseFloat(__ENV.SEND_PROB || '0.1');
const SEND_INTERVAL_MS = parseInt(__ENV.SEND_INTERVAL_MS || '15000', 10);
const DM_PEER_ID  = __ENV.DM_PEER_ID ? parseInt(__ENV.DM_PEER_ID, 10) : 0;

// Custom metrics.
const pushLatency = new Trend('im_push_latency_ms', true);
const wsConnErrors = new Counter('im_ws_connect_errors');
const wsPushReceived = new Counter('im_ws_push_received');
const wsPushOK = new Rate('im_ws_push_ok');

export const options = {
  scenarios: {
    websocket_ramp: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '2m', target: Math.floor(TARGET_VUS * 0.33) },
        { duration: '5m', target: TARGET_VUS },
        { duration: '3m', target: TARGET_VUS },
        { duration: '1m', target: 0 },
      ],
      gracefulRampDown: '30s',
    },
  },
  thresholds: {
    // p99 < 80ms for cross-pod push (OVERALL §5.5 S5).
    im_push_latency_ms: ['p(99)<80'],
    http_req_duration:  ['p(99)<200'],
    im_ws_connect_errors: ['count<1000'],
  },
  // k6 defaults are too conservative for WS soak tests at this scale.
  discardResponseBodies: true,
  noConnectionReuse: false,
};

function login(username) {
  const body = JSON.stringify({ login: username, password: PASSWORD });
  const r = http.post(`${API_BASE}/api/auth/login`, body,
    { headers: { 'Content-Type': 'application/json' } });
  if (r.status === 200) return r.json('token');
  // fall back to register — parallel VUs may 409 on 'username taken', that's OK.
  const regBody = JSON.stringify({
    username, email: `${username}@v4.load`, password: PASSWORD, display_name: username,
  });
  const rr = http.post(`${API_BASE}/api/auth/register`, regBody,
    { headers: { 'Content-Type': 'application/json' } });
  if (rr.status === 201) return rr.json('token');
  if (rr.status === 409) {
    // try login once more
    const r2 = http.post(`${API_BASE}/api/auth/login`, body,
      { headers: { 'Content-Type': 'application/json' } });
    if (r2.status === 200) return r2.json('token');
  }
  return null;
}

export default function () {
  const username = `${USER_PREFIX}${__VU}`;
  const token = login(username);
  if (!token) {
    wsConnErrors.add(1);
    sleep(1);
    return;
  }

  const url = `${WS_BASE}/ws?token=${token}&device=v4-load-${__VU}`;
  const shouldSend = DM_PEER_ID > 0 && Math.random() < SEND_PROB;

  const res = ws.connect(url, {}, (socket) => {
    socket.on('open', () => {
      // Heartbeat every 15s keeps the hub's idle-cleanup from reaping us.
      socket.setInterval(() => {
        socket.send(JSON.stringify({
          type: 'ping',
          payload: Buffer.from(JSON.stringify({ channel_seqs: {} })).toString('base64'),
        }));
      }, 15000);

      if (shouldSend) {
        socket.setInterval(() => {
          const payload = {
            client_msg_id: `v4-${__VU}-${Date.now()}`,
            channel_id: DM_PEER_ID, // operator must pre-create DM
            content: `load ${__VU} ${Date.now()}`,
            msg_type: 1,
          };
          socket.send(JSON.stringify({
            type: 'send',
            payload: Buffer.from(JSON.stringify(payload)).toString('base64'),
          }));
        }, SEND_INTERVAL_MS);
      }
    });

    socket.on('message', (data) => {
      let f;
      try { f = JSON.parse(data); } catch (_) { return; }
      if (f.type === 'push_msg') {
        wsPushReceived.add(1);
        wsPushOK.add(true);
        // If the payload embeds a created_at timestamp we could measure
        // end-to-end latency. The server sends it; decode and diff.
        try {
          const p = JSON.parse(Buffer.from(f.payload, 'base64').toString('utf-8'));
          if (p.created_at) {
            const sent = Date.parse(p.created_at);
            if (!isNaN(sent)) pushLatency.add(Date.now() - sent);
          }
        } catch (_) { /* ignore */ }
      }
    });

    socket.on('error', (e) => { wsConnErrors.add(1); });

    // Keep each VU's socket open for the duration of the iteration.
    socket.setTimeout(() => socket.close(), 10 * 60 * 1000);
  });

  check(res, { 'ws connected': (r) => r && r.status === 101 });
}
