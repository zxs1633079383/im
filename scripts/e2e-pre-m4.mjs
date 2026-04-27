#!/usr/bin/env node
/**
 * e2e-pre-m4.mjs — cookie-auth end-to-end harness for the M4 surface.
 *
 * Replaces the JWT/WS half of e2e-pre.mjs:
 *   - register/login → 410 in M4, so we seed a Mattermost cookieId fixture
 *     into the upstream cses Redis HASH "User" via redis-cli (one VU per
 *     user) and authenticate by stamping the cookieId header.
 *   - WS handler still validates JWT (?token=<jwt>) — that hand-off is the
 *     remaining piece for v0.6.3 (ws_handler.go cookie cut-over). Until
 *     then this harness exercises the HTTP surface only and the
 *     WS-dependent G2/G3/G4/G7/G8/G9 events from e2e-pre.mjs are
 *     deliberately skipped.
 *
 * Coverage (HTTP only):
 *   G1.send       POST /api/channels/:id/messages           201
 *   G2.markRead   POST /api/channels/:id/read               200
 *   G3.delete     DELETE /api/messages/:id                  200
 *   G4.edit       PATCH /api/messages/:id                   200
 *   G5.sync.delta POST /api/sync (cursor=0)                  has channel
 *   G6.sync.empty POST /api/sync (cursor=server_seq)         empty
 *   G7.update     PUT /api/channels/:id                     200
 *   G8.add_member POST /api/channels/:id/members            201/200
 *   G9.friend     POST /api/friends/request                 200/201
 *   G10.list      GET /api/friends                          200
 *
 * Usage:
 *   IM_GATEWAY=http://localhost:38080 \
 *   IM_REDIS=localhost:26379 \
 *     node scripts/e2e-pre-m4.mjs
 *
 * Exit codes: 0 all green, 1 any assertion failed, 2 fatal setup error.
 */

import { execSync } from 'child_process';

const GATEWAY = (process.env.IM_GATEWAY || 'http://localhost:38080').replace(/\/$/, '');
const REDIS   = process.env.IM_REDIS || 'localhost:26379';
const COMPANY = process.env.COMPANY_ID || '6111fb0a202d425d221c53db';

// Three deterministic test identities. 24-char hex satisfies auth.ValidateUserID.
const ALICE = { cookieId: 'aaaae2eaaaaaaaaaaaaaaaa1', userId: 'bbbae2ebbbbbbbbbbbbbbbb1', name: 'e2e-alice' };
const BOB   = { cookieId: 'aaaae2eaaaaaaaaaaaaaaaa2', userId: 'bbbae2ebbbbbbbbbbbbbbbb2', name: 'e2e-bob' };
const CAROL = { cookieId: 'aaaae2eaaaaaaaaaaaaaaaa3', userId: 'bbbae2ebbbbbbbbbbbbbbbb3', name: 'e2e-carol' };

const results = [];
function step(name, ok, detail) {
    results.push({ name, ok, detail });
    console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail ? ' — ' + detail : ''}`);
}

function seedCookie(u) {
    const body = JSON.stringify({
        id: u.userId, userId: u.userId,
        userName: u.name, name: u.name,
        companyId: COMPANY, orgId: COMPANY,
        roles: ['Member'], orgRole: 'Member',
    });
    const [host, port] = REDIS.split(':');
    execSync(`redis-cli -h ${host} -p ${port} HSET User '"${u.cookieId}"' '${body.replace(/'/g, `'\\''`)}' >/dev/null`);
}

async function http(method, path, { body, cookie } = {}) {
    const headers = {};
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (cookie) headers['cookieId'] = cookie;
    const r = await fetch(GATEWAY + path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await r.text();
    let parsed = null;
    try { parsed = text ? JSON.parse(text) : null; } catch { /* keep raw */ }
    return { status: r.status, body: parsed, raw: text };
}

async function main() {
    [ALICE, BOB, CAROL].forEach(seedCookie);
    step('setup.seed_cookies', true, '3 fixtures into Redis HASH User');

    const me = await http('GET', '/api/auth/me', { cookie: ALICE.cookieId });
    if (me.status !== 200) {
        step('setup.auth_resolves', false, `/me ${me.status} ${me.raw}`);
        process.exit(2);
    }
    step('setup.auth_resolves', me.body.userId === ALICE.userId,
        `alice userId=${me.body.userId}`);

    const grp = await http('POST', '/api/channels', {
        cookie: ALICE.cookieId,
        body: { name: `e2e-m4-${Date.now()}`, member_ids: [BOB.userId] },
    });
    if (grp.status !== 201) {
        step('setup.channel', false, `${grp.status} ${grp.raw}`);
        process.exit(2);
    }
    const channelId = grp.body.id;
    step('setup.channel', true, `channel_id=${channelId}`);

    const sent = await http('POST', `/api/channels/${channelId}/messages`, {
        cookie: ALICE.cookieId,
        body: { content: 'hello from alice', client_msg_id: 'e2e-m4-g1', msg_type: 1 },
    });
    step('G1.send', sent.status === 201 && sent.body?.sender_id === ALICE.userId,
        `http=${sent.status} sender=${sent.body?.sender_id}`);
    const msgId = sent.body?.id;
    const seq1 = sent.body?.seq;

    const read = await http('POST', `/api/channels/${channelId}/read`, {
        cookie: BOB.cookieId,
        body: { seq: seq1 },
    });
    step('G2.markRead', read.status === 200, `http=${read.status}`);

    const del = await http('DELETE', `/api/messages/${msgId}`, { cookie: ALICE.cookieId });
    step('G3.delete', del.status === 200, `http=${del.status}`);

    const sent2 = await http('POST', `/api/channels/${channelId}/messages`, {
        cookie: ALICE.cookieId,
        body: { content: 'pre-edit', client_msg_id: 'e2e-m4-g4', msg_type: 1 },
    });
    const edit = await http('PATCH', `/api/messages/${sent2.body.id}`, {
        cookie: ALICE.cookieId,
        body: { content: 'edited' },
    });
    step('G4.edit', edit.status === 200, `http=${edit.status}`);

    const sync1 = await http('POST', '/api/sync', {
        cookie: BOB.cookieId,
        body: { channels: [{ id: channelId, seq: 0 }] },
    });
    const has = Array.isArray(sync1.body?.channels) &&
                sync1.body.channels.some(c => c.id === channelId);
    step('G5.sync.with_delta', sync1.status === 200 && has,
        `chans=${sync1.body?.channels?.length ?? 0}`);

    const latest = sync1.body.channels.find(c => c.id === channelId)?.server_seq ?? 0;
    const sync2 = await http('POST', '/api/sync', {
        cookie: BOB.cookieId,
        body: { channels: [{ id: channelId, seq: latest }] },
    });
    const empty = Array.isArray(sync2.body?.channels) && sync2.body.channels.length === 0;
    step('G6.sync.empty', sync2.status === 200 && empty,
        `chans=${sync2.body?.channels?.length ?? 0}`);

    const upd = await http('PUT', `/api/channels/${channelId}`, {
        cookie: ALICE.cookieId,
        body: { name: 'renamed-m4', avatar_url: '' },
    });
    step('G7.channel.update', upd.status === 200, `http=${upd.status}`);

    const addM = await http('POST', `/api/channels/${channelId}/members`, {
        cookie: ALICE.cookieId,
        body: { user_id: CAROL.userId },
    });
    step('G8.member.add', addM.status === 201 || addM.status === 200,
        `http=${addM.status}`);

    const fr = await http('POST', '/api/friends/request', {
        cookie: ALICE.cookieId,
        body: { addressee_id: BOB.userId },
    });
    step('G9.friend.request', fr.status === 200 || fr.status === 201 || fr.status === 409,
        `http=${fr.status}`);

    const fl = await http('GET', '/api/friends', { cookie: ALICE.cookieId });
    step('G10.friend.list', fl.status === 200, `http=${fl.status}`);

    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} green`);
    if (failed.length > 0) {
        console.log('FAILED:', failed.map(f => f.name).join(', '));
        process.exit(1);
    }
}

main().catch(err => {
    console.error('FATAL', err);
    process.exit(2);
});
