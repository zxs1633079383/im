#!/usr/bin/env node
/**
 * ws-smoke-zhanglichao.mjs — 用真张立超 cookie (testutil.RealCookieID) 跑
 * v0.6.3 WS 三种鉴权方式，确认 production fixture 在 /ws 上端到端走通。
 *
 * Usage: IM_GATEWAY=http://localhost:38080 node ws-smoke-zhanglichao.mjs
 */
import WebSocket from 'ws';

const HTTP = (process.env.IM_GATEWAY || 'http://localhost:38080').replace(/\/$/, '');
const WS = HTTP.replace(/^http/, 'ws');

// 张立超 production fixture（与 testutil.RealCookieID 完全一致）
const REAL = {
    cookieId: '69eec6dbe6876865ff98945a',
    userId:   '676cc4ccfbbc501161d5cd65',
    name:     '张立超',
};

const results = [];
function step(name, ok, detail) {
    results.push({ name, ok, detail });
    console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}${detail ? ' — ' + detail : ''}`);
}

function openWS(url, headers = {}) {
    return new Promise((resolve, reject) => {
        const ws = new WebSocket(url, { headers });
        const timer = setTimeout(() => reject(new Error('open timeout')), 5000);
        ws.on('open', () => { clearTimeout(timer); resolve(ws); });
        ws.on('error', (e) => { clearTimeout(timer); reject(e); });
    });
}

async function main() {
    console.log(`testing 张立超 cookie ${REAL.cookieId} on ${WS}/ws`);
    console.log(`expecting userId resolve to ${REAL.userId}\n`);

    // 1. Header CookieId
    try {
        const ws = await openWS(`${WS}/ws`, { CookieId: REAL.cookieId });
        step('1.header_CookieId', true, 'WS upgraded');
        ws.close();
    } catch (e) {
        step('1.header_CookieId', false, e.message);
    }

    // 2. Header lowercase cookieid
    try {
        const ws = await openWS(`${WS}/ws`, { cookieid: REAL.cookieId });
        step('2.header_cookieid_lowercase', true, 'WS upgraded');
        ws.close();
    } catch (e) {
        step('2.header_cookieid_lowercase', false, e.message);
    }

    // 3. Query ?cookieId=
    try {
        const ws = await openWS(`${WS}/ws?cookieId=${REAL.cookieId}`);
        step('3.query_cookieId', true, 'WS upgraded');
        ws.close();
    } catch (e) {
        step('3.query_cookieId', false, e.message);
    }

    // 4. Query ?cookie_id= (snake case alt)
    try {
        const ws = await openWS(`${WS}/ws?cookie_id=${REAL.cookieId}`);
        step('4.query_cookie_id', true, 'WS upgraded');
        ws.close();
    } catch (e) {
        step('4.query_cookie_id', false, e.message);
    }

    // 5. 错的 cookie 必须 401
    try {
        await openWS(`${WS}/ws?cookieId=ffffffffffffffffffffffff`);
        step('5.bad_cookie_rejected', false, 'expected 401, got upgrade');
    } catch (e) {
        const ok = /401|invalid cookieId/i.test(e.message);
        step('5.bad_cookie_rejected', ok, e.message);
    }

    const failed = results.filter(r => !r.ok);
    console.log(`\n${results.length - failed.length}/${results.length} green`);
    if (failed.length > 0) {
        console.log('FAILED:', failed.map(f => f.name).join(', '));
        process.exit(1);
    }
}
main().catch(e => { console.error('FATAL', e); process.exit(2); });
