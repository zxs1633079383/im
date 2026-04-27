#!/usr/bin/env node
/**
 * v072-smoke.mjs — modules + online-status batch + WS push_msg → snake_case 验证。
 */
import { execSync } from 'child_process';
import WebSocket from 'ws';

const HTTP = (process.env.IM_GATEWAY || 'http://localhost:38080').replace(/\/$/, '');
const WS_URL = HTTP.replace(/^http/, 'ws');
const REDIS = process.env.IM_REDIS || 'localhost:26379';
const COMPANY = '6111fb0a202d425d221c53db';

const ALICE = { cookieId: 'aaaa72aaaaaaaaaaaaaaaaa1', userId: 'bbbb72bbbbbbbbbbbbbbbbb1', name: 'v072-alice' };
const BOB   = { cookieId: 'aaaa72aaaaaaaaaaaaaaaaa2', userId: 'bbbb72bbbbbbbbbbbbbbbbb2', name: 'v072-bob' };

const results = [];
const step = (name, ok, detail) => {
    results.push({ name, ok, detail });
    console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail ? ' — ' + detail : ''}`);
};

const seedCookie = (u) => {
    const body = JSON.stringify({
        id: u.userId, userId: u.userId, userName: u.name, name: u.name,
        companyId: COMPANY, orgId: COMPANY, roles: ['Member'], orgRole: 'Member',
    });
    const [h, p] = REDIS.split(':');
    execSync(`redis-cli -h ${h} -p ${p} HSET User '"${u.cookieId}"' '${body.replace(/'/g, `'\\''`)}' >/dev/null`);
};

async function http(method, path, { body, cookie } = {}) {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (cookie) headers['cookieId'] = cookie;
    const r = await fetch(HTTP + path, {
        method, headers, body: body === undefined ? undefined : JSON.stringify(body),
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
    step('setup.seed', true);

    // ---- 1. /api/modules 返回 6 行 ----
    const mods = await http('GET', '/api/modules', { cookie: ALICE.cookieId });
    const okMods = mods.status === 200 && Array.isArray(mods.body) && mods.body.length === 6 &&
        mods.body.every(m => m.name && m.label && m.url && m.id);
    step('modules.list', okMods, `count=${mods.body?.length}`);

    // ---- 2. setup: alice 创建 DM 给 bob，bob 接 WS 进 routing ----
    const dm = await http('POST', '/api/channels/dm', {
        cookie: ALICE.cookieId, body: { peer_id: BOB.userId },
    });
    if (dm.status !== 200 && dm.status !== 201) {
        step('setup.dm', false, `${dm.status}`);
        process.exit(2);
    }
    const channelID = dm.body.id;
    step('setup.dm', true, `channel=${channelID}`);

    // bob 上线（接 WS）
    const bobWS = await openWS(`${WS_URL}/ws`, { CookieId: BOB.cookieId });
    step('setup.bob_ws', true, 'bob WS upgraded → routing.Register');

    // 给 routing 一点时间落 redis
    await new Promise(r => setTimeout(r, 200));

    // ---- 3. online-status batch — bob 在线 alice 离线 ----
    const status = await http('GET', `/api/channels/online-status?channel_ids=${channelID}&include_users=true`, {
        cookie: ALICE.cookieId,
    });
    const entry = (status.body?.channels ?? [])[0];
    const okStatus = status.status === 200 &&
        entry?.channel_id === channelID &&
        entry?.online_count >= 1 &&
        Array.isArray(entry?.online_user_ids) &&
        entry.online_user_ids.includes(BOB.userId);
    step('online-status.batch', okStatus,
        `online_count=${entry?.online_count} users=${JSON.stringify(entry?.online_user_ids)}`);

    // ---- 4. WS push_msg payload snake_case 验证 ----
    const sent = await http('POST', `/api/channels/${channelID}/messages`, {
        cookie: ALICE.cookieId,
        body: { content: 'v072-ws', client_msg_id: `v072-${Date.now()}`, msg_type: 1 },
    });
    const ev = await waitFor(bobWS.queue, m => m.type === 'push_msg');
    const okWS = sent.status === 201 &&
        ev?.payload?.channel_id === channelID &&
        ev?.payload?.sender_id === ALICE.userId &&
        ev?.payload?.content === 'v072-ws';
    step('ws.push_msg.snake_case', okWS,
        `channel_id=${ev?.payload?.channel_id} sender_id=${ev?.payload?.sender_id?.substring(0,8)}... content=${ev?.payload?.content}`);

    // ---- 5. WS reaction_added payload ----
    const messageID = sent.body.id;
    const reactAdd = await http('POST', `/api/messages/${messageID}/reactions`, {
        cookie: BOB.cookieId, body: { emoji: ':thumbsup:' },
    });
    const reactEv = await waitFor(bobWS.queue, m => m.type === 'reaction_added');
    const okReact = reactAdd.status === 201 &&
        reactEv?.payload?.message_id === messageID &&
        reactEv?.payload?.user_id === BOB.userId &&
        reactEv?.payload?.emoji === ':thumbsup:';
    step('ws.reaction_added.snake_case', okReact,
        `message_id=${reactEv?.payload?.message_id} emoji=${reactEv?.payload?.emoji}`);

    bobWS.ws.close();
    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} green`);
    if (failed.length > 0) {
        console.log('FAILED:', failed.map(f => f.name).join(', '));
        process.exit(1);
    }
}
main().catch(e => { console.error('FATAL', e); process.exit(2); });
