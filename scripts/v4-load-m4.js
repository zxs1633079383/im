// v4-load-m4.js — k6 load test for the M4 cookie-only auth surface.
//
// Replaces v4-load.js (M3 era /register + /login + JWT). Each VU adopts
// one (cookieId, userId) pair from /scripts/cookies.csv (mounted ConfigMap
// produced by seed-mm-cookies-bulk.sh) and exercises the read+write hot
// path:
//
//   1. GET  /api/auth/me                     — auth warm-up + LRU hit/miss
//   2. POST /api/channels/dm  {peer_id}      — create-or-get DM
//   3. POST /api/channels/:id/messages       — send (the SLO target)
//   4. POST /api/sync                        — cursor=0 readback
//
// pre-6 baseline: send P95 = 375ms at VU=300. M4 LRU adds ≤ 1 Redis HGET
// on cold cookie + 30s warm cache hit, so the SLO bumps to 400ms (25ms
// budget for the cache lookup).
//
// Env knobs:
//   API_BASE           default http://im-gateway:8080 (in-cluster)
//   TARGET_VUS         default 300
//   RAMP_SEC           default 60
//   SOAK_SEC           default 180
//   DOWN_SEC           default 30
//   SEND_PER_ITER      default 3   (messages sent per VU iteration)
//   COOKIE_CSV         default /scripts/cookies.csv

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';

const API_BASE      = __ENV.API_BASE || 'http://im-gateway:8080';
const TARGET_VUS    = parseInt(__ENV.TARGET_VUS || '300', 10);
const RAMP_SEC      = parseInt(__ENV.RAMP_SEC || '60', 10);
const SOAK_SEC      = parseInt(__ENV.SOAK_SEC || '180', 10);
const DOWN_SEC      = parseInt(__ENV.DOWN_SEC || '30', 10);
const SEND_PER_ITER = parseInt(__ENV.SEND_PER_ITER || '3', 10);
const COOKIE_CSV    = __ENV.COOKIE_CSV || '/scripts/cookies.csv';

// Loaded at init (open() only works in init context). Each row =
// "cookieId,userId". Newlines split; trailing blank lines are filtered.
const COOKIE_POOL = open(COOKIE_CSV)
    .split('\n')
    .map(l => l.trim())
    .filter(l => l.length > 0)
    .map(l => {
        const [cookieId, userId] = l.split(',');
        return { cookieId, userId };
    });

if (COOKIE_POOL.length < 2) {
    throw new Error(`cookie pool too small (${COOKIE_POOL.length}); seed at least 2`);
}

const meLatency   = new Trend('im_me_ms', true);
const sendLatency = new Trend('im_send_ms', true);
const syncLatency = new Trend('im_sync_ms', true);
const dmCreated   = new Counter('im_dm_created');
const sendOk      = new Rate('im_send_ok');
const actionOk    = new Rate('im_action_ok');

export const options = {
    scenarios: {
        cookie_send: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: `${RAMP_SEC}s`, target: TARGET_VUS },
                { duration: `${SOAK_SEC}s`, target: TARGET_VUS },
                { duration: `${DOWN_SEC}s`, target: 0 },
            ],
            gracefulRampDown: '15s',
        },
    },
    thresholds: {
        // SLO: send P95 ≤ 400ms (pre-6 baseline 375ms + 25ms LRU budget).
        'im_send_ms': ['p(95)<400'],
        // ≥99% successful actions overall.
        'im_action_ok': ['rate>0.99'],
        'im_send_ok':   ['rate>0.99'],
    },
};

// pickPeer — round-robin into the cookie pool, skipping self so DM has
// two distinct identities.
function pickPeer(self) {
    let i = (__VU + __ITER) % COOKIE_POOL.length;
    if (COOKIE_POOL[i].userId === self.userId) {
        i = (i + 1) % COOKIE_POOL.length;
    }
    return COOKIE_POOL[i];
}

export default function () {
    const me = COOKIE_POOL[(__VU - 1) % COOKIE_POOL.length];
    const headers = { cookieId: me.cookieId, 'Content-Type': 'application/json' };

    // 1. /me — exercises cookie LRU path (warm cache after first hit).
    const meResp = http.get(`${API_BASE}/api/auth/me`, { headers, tags: { ep: 'me' } });
    meLatency.add(meResp.timings.duration);
    const meOk = check(meResp, { 'me 200': r => r.status === 200 });
    actionOk.add(meOk);
    if (!meOk) { sleep(1); return; }

    // 2. DM create-or-get.
    const peer = pickPeer(me);
    const dmResp = http.post(`${API_BASE}/api/channels/dm`,
        JSON.stringify({ peer_id: peer.userId }), { headers, tags: { ep: 'dm' } });
    const dmOk = check(dmResp, { 'dm 200/201': r => r.status === 200 || r.status === 201 });
    actionOk.add(dmOk);
    if (!dmOk) { sleep(1); return; }
    if (dmResp.status === 201) dmCreated.add(1);
    const channelID = dmResp.json('id');

    // 3. Send N messages (the SLO measurement loop).
    for (let i = 0; i < SEND_PER_ITER; i++) {
        const body = JSON.stringify({
            content: `k6-${__VU}-${__ITER}-${i}`,
            msg_type: 1,
            client_msg_id: `k6-${__VU}-${__ITER}-${i}`,
        });
        const sendResp = http.post(`${API_BASE}/api/channels/${channelID}/messages`,
            body, { headers, tags: { ep: 'send' } });
        sendLatency.add(sendResp.timings.duration);
        const ok = check(sendResp, { 'send 201': r => r.status === 201 });
        sendOk.add(ok);
        actionOk.add(ok);
    }

    // 4. /sync from cursor=0 — confirms read path matches write seq.
    const syncBody = JSON.stringify({ channels: [{ id: channelID, seq: 0 }] });
    const syncResp = http.post(`${API_BASE}/api/sync`, syncBody, { headers, tags: { ep: 'sync' } });
    syncLatency.add(syncResp.timings.duration);
    actionOk.add(check(syncResp, { 'sync 200': r => r.status === 200 }));

    sleep(0.5);
}

export function handleSummary(data) {
    return { stdout: textSummary(data) };
}

// textSummary — minimal ANSI-free summary for kubectl logs readability.
function textSummary(data) {
    const m = data.metrics;
    const t = (name) => {
        const v = m[name] && m[name].values;
        if (!v) return `${name}: n/a`;
        const p95 = (v['p(95)'] || 0).toFixed(0);
        const avg = (v.avg       || 0).toFixed(0);
        return `${name}: avg=${avg}ms p95=${p95}ms`;
    };
    const r = (name) => {
        const v = m[name] && m[name].values;
        if (!v) return `${name}: n/a`;
        return `${name}: ${(v.rate * 100).toFixed(2)}%`;
    };
    return [
        '=== M4 cookie-load summary ===',
        `cookie_pool=${COOKIE_POOL.length}, vus=${TARGET_VUS}`,
        t('im_me_ms'),
        t('im_send_ms'),
        t('im_sync_ms'),
        r('im_send_ok'),
        r('im_action_ok'),
        `dm_created: ${(m.im_dm_created && m.im_dm_created.values.count) || 0}`,
        '',
    ].join('\n');
}
