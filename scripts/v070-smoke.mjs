#!/usr/bin/env node
/**
 * v070-smoke.mjs — verify the v0.7.0 endpoints (reactions / batch send /
 * messages-after / channel governance extras / per-user is_top).
 *
 * Usage: IM_GATEWAY=http://localhost:38080 IM_REDIS=localhost:26379 \
 *          node scripts/v070-smoke.mjs
 */
import { execSync } from 'child_process';

const HTTP = (process.env.IM_GATEWAY || 'http://localhost:38080').replace(/\/$/, '');
const REDIS = process.env.IM_REDIS || 'localhost:26379';
const COMPANY = '6111fb0a202d425d221c53db';

const ALICE = { cookieId: 'aaaa70aaaaaaaaaaaaaaaaa1', userId: 'bbbb70bbbbbbbbbbbbbbbbb1', name: 'v070-alice' };
const BOB   = { cookieId: 'aaaa70aaaaaaaaaaaaaaaaa2', userId: 'bbbb70bbbbbbbbbbbbbbbbb2', name: 'v070-bob' };

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

async function main() {
    [ALICE, BOB].forEach(seedCookie);
    step('setup.seed', true);

    // -- create DM and send a message to react on --
    const dm = await http('POST', '/api/channels/dm', {
        cookie: ALICE.cookieId, body: { peer_id: BOB.userId },
    });
    if (dm.status !== 200 && dm.status !== 201) {
        step('setup.dm', false, `${dm.status} ${dm.raw}`);
        process.exit(2);
    }
    const channelID = dm.body.id;
    step('setup.dm', true, `channel=${channelID}`);

    const sent = await http('POST', `/api/channels/${channelID}/messages`, {
        cookie: ALICE.cookieId,
        body: { content: 'v070-react-target', client_msg_id: `v070-${Date.now()}`, msg_type: 1 },
    });
    if (sent.status !== 201) {
        step('setup.send', false, `${sent.status}`);
        process.exit(2);
    }
    const messageID = sent.body.id;
    step('setup.send', true, `message=${messageID}`);

    // ---- 1. reaction add ----
    const add = await http('POST', `/api/messages/${messageID}/reactions`, {
        cookie: BOB.cookieId, body: { emoji: ':thumbsup:' },
    });
    step('reaction.add', add.status === 201, `http=${add.status}`);

    // ---- 2. reaction list (alice as channel member) ----
    const list = await http('GET', `/api/messages/${messageID}/reactions`, { cookie: ALICE.cookieId });
    const okList = list.status === 200 &&
        Array.isArray(list.body) && list.body.length === 1 &&
        list.body[0].emoji === ':thumbsup:' && list.body[0].user_id === BOB.userId;
    step('reaction.list', okList, `count=${list.body?.length}`);

    // ---- 3. reaction remove ----
    const rem = await http('DELETE', `/api/messages/${messageID}/reactions/${encodeURIComponent(':thumbsup:')}`, {
        cookie: BOB.cookieId,
    });
    step('reaction.remove', rem.status === 200, `http=${rem.status}`);

    // ---- 4. reaction.remove again — must 404 ----
    const remAgain = await http('DELETE', `/api/messages/${messageID}/reactions/${encodeURIComponent(':thumbsup:')}`, {
        cookie: BOB.cookieId,
    });
    step('reaction.remove_idempotent_404', remAgain.status === 404, `http=${remAgain.status}`);

    // ---- 5. messages.after — alice posts another, bob does /after ----
    const m2 = await http('POST', `/api/channels/${channelID}/messages`, {
        cookie: ALICE.cookieId,
        body: { content: 'after-target', client_msg_id: `v070-after-${Date.now()}`, msg_type: 1 },
    });
    const after = await http('GET', `/api/messages/${messageID}/after?limit=10`, { cookie: BOB.cookieId });
    const okAfter = after.status === 200 &&
        Array.isArray(after.body?.messages) &&
        after.body.messages.some(m => m.id === m2.body.id);
    step('messages.after', okAfter, `count=${after.body?.messages?.length} found=${okAfter}`);

    // ---- 6. batch send: alice broadcasts to [channelID] (single channel for smoke) ----
    const batch = await http('POST', '/api/messages/batch', {
        cookie: ALICE.cookieId,
        body: {
            channel_ids: [channelID],
            content: 'v070-batch',
            msg_type: 1,
            client_msg_id: `v070-batch-${Date.now()}`,
        },
    });
    const okBatch = batch.status === 201 &&
        Array.isArray(batch.body?.messages) &&
        batch.body.messages.length === 1 &&
        batch.body.messages[0].channel_id === channelID;
    step('messages.batch', okBatch, `inserted=${batch.body?.messages?.length}`);

    // ---- 7. channel patch — alice sets notice + purpose via existing PATCH ----
    const patch = await http('PUT', `/api/channels/${channelID}`, {
        cookie: ALICE.cookieId,
        body: { name: 'v070-renamed', avatar_url: '' },
    });
    step('channel.patch.name', patch.status === 200, `http=${patch.status}`);

    // (PATCH /api/channels/:id 已支持 notice/purpose/orient/permission，
    // 但 Channel handler 当前只在 Update 实现 name+avatar_url；governance.PATCH
    // 才有完整列表 — 先测可用部分。Stage 1 前端切到 governance.PATCH 即可。)

    // ---- 8. is_top per-user ----
    const topOn = await http('PATCH', `/api/channels/${channelID}/members/${ALICE.userId}`, {
        cookie: ALICE.cookieId, body: { is_top: true },
    });
    step('channel.member.is_top.set', topOn.status === 200, `http=${topOn.status}`);

    const topSelfOnly = await http('PATCH', `/api/channels/${channelID}/members/${BOB.userId}`, {
        cookie: ALICE.cookieId, body: { is_top: true },  // alice trying to pin BOB's view
    });
    step('channel.member.is_top.self_only_403', topSelfOnly.status === 403, `http=${topSelfOnly.status}`);

    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} green`);
    if (failed.length > 0) {
        console.log('FAILED:', failed.map(f => f.name).join(', '));
        process.exit(1);
    }
}
main().catch(e => { console.error('FATAL', e); process.exit(2); });
