#!/usr/bin/env node
/**
 * ws-smoke-m4.mjs — verify the v0.6.3 WS cookie-auth contract.
 *
 * Three scenarios, each must produce the documented outcome:
 *
 *   1. Header  CookieId  → upgrade succeeds (message-v3 wire shape)
 *   2. Query   ?cookieId= → upgrade succeeds (browser fallback)
 *   3. No auth at all     → server replies 401, no upgrade
 *
 * Bonus: with two distinct cookies, send a HTTP message on user A and
 * watch user B receive a `push_msg` over WS — proves the cookie auth
 * actually populates routing.Register so cross-conn fan-out works.
 *
 * Usage:
 *   IM_GATEWAY=http://localhost:38080 \
 *   IM_REDIS=localhost:26379 \
 *     node scripts/ws-smoke-m4.mjs
 *
 * Exit 0 = all green, 1 = any case failed, 2 = setup error.
 */

import { execSync } from 'child_process';
import WebSocket from 'ws';

const HTTP  = (process.env.IM_GATEWAY || 'http://localhost:38080').replace(/\/$/, '');
const WS    = HTTP.replace(/^http/, 'ws');
const REDIS = process.env.IM_REDIS || 'localhost:26379';
const COMPANY = '6111fb0a202d425d221c53db';

const ALICE = { cookieId: 'aaaa6e036aaaaaaaaaaaaaaa', userId: 'bbbb6e036bbbbbbbbbbbbbbb', name: 'ws-alice' };
const BOB   = { cookieId: 'aaaa6e036aaaaaaaaaaaaaab', userId: 'bbbb6e036bbbbbbbbbbbbbbc', name: 'ws-bob' };

const results = [];
function step(name, ok, detail) {
    results.push({ name, ok, detail });
    console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail ? ' — ' + detail : ''}`);
}

function seedCookie(u) {
    const body = JSON.stringify({
        id: u.userId, userId: u.userId, userName: u.name, name: u.name,
        companyId: COMPANY, orgId: COMPANY, roles: ['Member'], orgRole: 'Member',
    });
    const [host, port] = REDIS.split(':');
    execSync(`redis-cli -h ${host} -p ${port} HSET User '"${u.cookieId}"' '${body.replace(/'/g, `'\\''`)}' >/dev/null`);
}

async function http(method, path, { body, cookie } = {}) {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (cookie) headers['cookieId'] = cookie;
    const r = await fetch(HTTP + path, {
        method, headers,
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    let parsed = null;
    const text = await r.text();
    try { parsed = text ? JSON.parse(text) : null; } catch { /* keep raw */ }
    return { status: r.status, body: parsed, raw: text };
}

function openWS(url, headers = {}) {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(url, { headers });
        const queue = [];
        const timer = setTimeout(() => reject(new Error('open timeout')), 5000);
        ws.on('open', () => { clearTimeout(timer); resolve({ ws, queue }); });
        ws.on('error', (e) => { clearTimeout(timer); reject(e); });
        ws.on('message', (raw) => {
            try { queue.push(JSON.parse(raw.toString())); } catch { /* skip */ }
        });
    });
}

function waitFor(queue, predicate, timeoutMs = 4000) {
    return new Promise((resolve) => {
        const t0 = Date.now();
        const tick = () => {
            const idx = queue.findIndex(predicate);
            if (idx >= 0) { const m = queue[idx]; queue.splice(idx, 1); return resolve(m); }
            if (Date.now() - t0 > timeoutMs) return resolve(null);
            setTimeout(tick, 50);
        };
        tick();
    });
}

async function main() {
    [ALICE, BOB].forEach(seedCookie);
    step('setup.seed', true, 'alice + bob cookies in Redis HASH User');

    // --- 1. Header CookieId ---
    let aSession, bSession;
    try {
        aSession = await openWS(`${WS}/ws`, { CookieId: ALICE.cookieId });
        step('1.header_cookie', true, 'WS upgraded with CookieId Header');
    } catch (e) {
        step('1.header_cookie', false, e.message);
    }

    // --- 2. Query ?cookieId= ---
    try {
        bSession = await openWS(`${WS}/ws?cookieId=${BOB.cookieId}`);
        step('2.query_cookie', true, 'WS upgraded with ?cookieId= query');
    } catch (e) {
        step('2.query_cookie', false, e.message);
    }

    // --- 3. No auth → 401 ---
    try {
        await openWS(`${WS}/ws`);
        step('3.no_auth_rejected', false, 'expected 401, got upgrade');
    } catch (e) {
        const ok = /401|unauthor|missing/i.test(e.message);
        step('3.no_auth_rejected', ok, e.message);
    }

    // --- 4. push_msg fan-out (alice posts → bob's WS sees push_msg) ---
    if (aSession && bSession) {
        const dm = await http('POST', '/api/channels/dm', {
            cookie: ALICE.cookieId,
            body: { peer_id: BOB.userId },
        });
        if (dm.status !== 200 && dm.status !== 201) {
            step('4.push_msg_fanout', false, `dm http=${dm.status}`);
        } else {
            const channelId = dm.body.id;
            const sent = await http('POST', `/api/channels/${channelId}/messages`, {
                cookie: ALICE.cookieId,
                body: { content: 'ws-smoke', client_msg_id: 'ws-smoke-1', msg_type: 1 },
            });
            const ev = await waitFor(bSession.queue, m => m.type === 'push_msg');
            const ok = sent.status === 201 && ev?.payload?.seq === sent.body.seq;
            step('4.push_msg_fanout', ok,
                `sent_seq=${sent.body?.seq} ws_seq=${ev?.payload?.seq}`);
        }
    } else {
        step('4.push_msg_fanout', false, 'previous session(s) failed');
    }

    aSession?.ws?.close();
    bSession?.ws?.close();

    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} green`);
    if (failed.length > 0) {
        console.log('FAILED:', failed.map(f => f.name).join(', '));
        process.exit(1);
    }
}

main().catch(err => { console.error('FATAL', err); process.exit(2); });
