// fullchain-load.js — HTTP + WS full-chain k6 script.
//
// Per VU lifecycle:
//   1. login (pre-seeded user_${VU}); no register stampede.
//   2. Open a WebSocket, heartbeat every 15s, record every push_msg.
//   3. Create/reuse a DM channel with peer_id = ((VU % PEER_POOL) + 1).
//   4. Action loop — per iteration pick one weighted action:
//        50% HTTP POST send       (→ expect push_msg on the WS)
//        15% HTTP POST markRead   (→ expect read_sync on the WS of the same user)
//        10% HTTP PATCH edit      (→ expect msg_updated broadcast)
//        10% HTTP DELETE          (→ expect msg_deleted broadcast)
//         9% HTTP POST sync       (→ HTTP-only response; no WS)
//         6% HTTP GET presence    (→ HTTP-only response; no WS)
//
// Metrics surface the per-action HTTP duration and E2E push_msg latency.
// Thresholds fail the run if error-rate climbs or p99 latency busts the SLO.
//
// Usage:
//   API_BASE=http://im-gateway:8080 WS_BASE=ws://im-gateway:8080 \
//   TARGET_VUS=100 V4_PASS=v4test1234 k6 run /scripts/fullchain-load.js

import ws from 'k6/ws';
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const API_BASE = __ENV.API_BASE || 'http://localhost:8080';
const WS_BASE  = __ENV.WS_BASE  || 'ws://localhost:8080';
const PASSWORD = __ENV.V4_PASS  || 'v4test1234';
const USER_PREFIX = __ENV.V4_USER_PREFIX || 'k6pre';
const TARGET_VUS  = parseInt(__ENV.TARGET_VUS || '100', 10);
const PEER_POOL   = parseInt(__ENV.PEER_POOL || '50', 10);
const RAMP_SEC    = parseInt(__ENV.RAMP_SEC || '30', 10);
const SOAK_SEC    = parseInt(__ENV.SOAK_SEC || '60', 10);
const DOWN_SEC    = parseInt(__ENV.DOWN_SEC || '30', 10);

// ---------- metrics ----------
const mSendDur    = new Trend('im_http_send_ms', true);
const mReadDur    = new Trend('im_http_markread_ms', true);
const mEditDur    = new Trend('im_http_edit_ms', true);
const mDeleteDur  = new Trend('im_http_delete_ms', true);
const mSyncDur    = new Trend('im_http_sync_ms', true);
const mPresDur    = new Trend('im_http_presence_ms', true);
const mPushE2E    = new Trend('im_push_e2e_ms', true);
const mPushReceived = new Counter('im_push_received');
const mWsErrors   = new Counter('im_ws_errors');
const mActionOK   = new Rate('im_action_ok');

export const options = {
    scenarios: {
        fullchain: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: `${RAMP_SEC}s`, target: TARGET_VUS },
                { duration: `${SOAK_SEC}s`, target: TARGET_VUS },
                { duration: `${DOWN_SEC}s`, target: 0 },
            ],
            gracefulRampDown: '10s',
        },
    },
    thresholds: {
        im_action_ok: ['rate>0.95'],
        im_http_send_ms: ['p(99)<500'],
        im_push_e2e_ms: ['p(99)<500'],
        im_ws_errors: ['count<100'],
    },
    discardResponseBodies: false,
    noConnectionReuse: false,
};

// ---------- helpers ----------
function post(path, body, jwt) {
    const headers = { 'Content-Type': 'application/json' };
    if (jwt) headers['Authorization'] = 'Bearer ' + jwt;
    return http.post(`${API_BASE}${path}`, JSON.stringify(body), { headers });
}
function get(path, jwt) {
    const headers = {};
    if (jwt) headers['Authorization'] = 'Bearer ' + jwt;
    return http.get(`${API_BASE}${path}`, { headers });
}
function del(path, jwt) {
    const headers = {};
    if (jwt) headers['Authorization'] = 'Bearer ' + jwt;
    return http.del(`${API_BASE}${path}`, null, { headers });
}
function patch(path, body, jwt) {
    const headers = { 'Content-Type': 'application/json' };
    if (jwt) headers['Authorization'] = 'Bearer ' + jwt;
    return http.patch(`${API_BASE}${path}`, JSON.stringify(body), { headers });
}

function login(username) {
    const r = http.post(`${API_BASE}/api/auth/login`,
        JSON.stringify({ login: username, password: PASSWORD }),
        { headers: { 'Content-Type': 'application/json' } });
    if (r && r.status === 200 && r.body) {
        try { return r.json('token'); } catch { return null; }
    }
    return null;
}

function me(jwt) {
    const r = get('/api/auth/me', jwt);
    if (r && r.status === 200 && r.body) {
        try { return r.json('id') || r.json('user.id'); } catch { return null; }
    }
    return null;
}

function ensureDM(jwt, peerUsername) {
    // Peer lookup via a simple username→id search would be cleanest, but we
    // know peers are seeded with the same prefix so their userIDs cluster
    // deterministically. Just POST a DM create with the peer USERNAME via
    // a search-by-username path if the backend supports it; otherwise accept
    // whatever ID we resolved from /me pool (see below).
    return null; // DM creation deferred to main loop via /api/channels/dm
}

// ---------- main VU ----------
export default function () {
    const username = `${USER_PREFIX}${__VU}`;
    const jwt = login(username);
    if (!jwt) { mWsErrors.add(1); sleep(1); return; }

    const myId = me(jwt);
    if (!myId) { mWsErrors.add(1); sleep(1); return; }

    // Peer = another seeded user. Use VU+1 (wraps around the pool).
    const peerVU = (__VU % PEER_POOL) + 1;
    if (peerVU === __VU) return; // safety: self-loop
    // Login peer to resolve its ID. Cheap since users are pre-seeded.
    const peerJwt = login(`${USER_PREFIX}${peerVU}`);
    if (!peerJwt) { mWsErrors.add(1); sleep(1); return; }
    const peerId = me(peerJwt);
    if (!peerId) { mWsErrors.add(1); sleep(1); return; }

    // Create-or-get DM channel.
    const dm = post('/api/channels/dm', { peer_id: peerId }, jwt);
    if (!dm || (dm.status !== 200 && dm.status !== 201)) {
        mWsErrors.add(1); sleep(1); return;
    }
    const channelId = dm.json('id');

    // Track our last-sent message for edit/delete targets.
    const sent = [];

    // Open WS and run the action loop inside the socket scope so push_msg
    // arrival maps to the iteration that sent it.
    const res = ws.connect(`${WS_BASE}/ws?token=${encodeURIComponent(jwt)}&device=fullchain-${__VU}`,
        {}, (socket) => {
        const sendTs = new Map();

        socket.on('open', () => {
            // Heartbeat ping.
            socket.setInterval(() => {
                socket.send(JSON.stringify({
                    type: 'ping',
                    payload: Buffer.from(JSON.stringify({ channel_seqs: {} })).toString('base64'),
                }));
            }, 15000);

            // Action loop — mixed HTTP actions driving the WS. Interval keeps
            // per-VU RPS bounded; scale with VU count at the scenario level.
            socket.setInterval(() => {
                pickAction(jwt, channelId, myId, peerId, sent, sendTs);
            }, 2000);
        });

        socket.on('message', (raw) => {
            let f;
            try { f = JSON.parse(raw); } catch { return; }
            if (f.type === 'push_msg') {
                mPushReceived.add(1);
                let p;
                try { p = JSON.parse(Buffer.from(f.payload, 'base64').toString('utf-8')); } catch { return; }
                if (p.client_msg_id && sendTs.has(p.client_msg_id)) {
                    mPushE2E.add(Date.now() - sendTs.get(p.client_msg_id));
                    sendTs.delete(p.client_msg_id);
                }
            }
        });
        socket.on('error', () => mWsErrors.add(1));

        // Close the socket at end-of-scenario soak.
        socket.setTimeout(() => socket.close(), (SOAK_SEC + DOWN_SEC + 5) * 1000);
    });
    check(res, { 'ws connected': (r) => r && r.status === 101 });
}

// pickAction performs exactly one weighted HTTP action per 2s tick.
function pickAction(jwt, channelId, myId, peerId, sent, sendTs) {
    const roll = Math.random();
    if (roll < 0.50) doSend(jwt, channelId, sent, sendTs);
    else if (roll < 0.65) doMarkRead(jwt, channelId);
    else if (roll < 0.75) doEdit(jwt, sent);
    else if (roll < 0.85) doDelete(jwt, sent);
    else if (roll < 0.94) doSync(jwt, channelId);
    else doPresence(jwt, channelId);
}

function doSend(jwt, channelId, sent, sendTs) {
    const cmid = `fc-${__VU}-${Date.now()}`;
    sendTs.set(cmid, Date.now());
    const t0 = Date.now();
    const r = post(`/api/channels/${channelId}/messages`,
        { content: `fc ${__VU} ${Date.now()}`, client_msg_id: cmid, msg_type: 1 }, jwt);
    mSendDur.add(Date.now() - t0);
    const ok = r && r.status === 201;
    mActionOK.add(ok);
    if (ok) {
        try { sent.push({ id: r.json('id'), seq: r.json('seq') }); } catch {}
        if (sent.length > 10) sent.shift();
    }
}
function doMarkRead(jwt, channelId) {
    const t0 = Date.now();
    const r = post(`/api/channels/${channelId}/read`, {}, jwt);
    mReadDur.add(Date.now() - t0);
    mActionOK.add(r && r.status === 200);
}
function doEdit(jwt, sent) {
    if (!sent.length) return;
    const target = sent[sent.length - 1];
    const t0 = Date.now();
    const r = patch(`/api/messages/${target.id}`, { content: `edited-${Date.now()}` }, jwt);
    mEditDur.add(Date.now() - t0);
    mActionOK.add(r && r.status === 200);
}
function doDelete(jwt, sent) {
    if (!sent.length) return;
    const target = sent.shift();
    const t0 = Date.now();
    const r = del(`/api/messages/${target.id}`, jwt);
    mDeleteDur.add(Date.now() - t0);
    mActionOK.add(r && r.status === 200);
}
function doSync(jwt, channelId) {
    const t0 = Date.now();
    const r = post('/api/sync', { channels: [{ id: channelId, seq: 0 }] }, jwt);
    mSyncDur.add(Date.now() - t0);
    mActionOK.add(r && r.status === 200);
}
function doPresence(jwt, channelId) {
    const t0 = Date.now();
    const r = get(`/api/presence?channel_id=${channelId}`, jwt);
    mPresDur.add(Date.now() - t0);
    mActionOK.add(r && r.status === 200);
}
