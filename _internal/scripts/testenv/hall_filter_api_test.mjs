#!/usr/bin/env node
// [122] 大厅敏感词屏蔽与告警邮件：接口 + WebSocket + 真实发信端到端测试（只连本机隔离测试后端 127.0.0.1:18080）。
// 前置：本机 SMTP 接收器 smtp_sink.mjs 已在 127.0.0.1:2525 运行（账号 qa / qa-pass，仅测试用）。
// 覆盖：未登录 401；设置读写（默认值、校验失败 40062、去重与域名规范化）；检测试用（含保存前预览）；
//       邮件设置（不返回密码、明文发外网 40063、测试邮件真实投递到接收器）；访客发联系方式/色情/网址 → 回执与广播均为屏蔽后内容、
//       拦截记录含原文与访客编号；发件箱合并发送告警邮件（正文含原文、北京时间）；正常消息不受影响；客服消息不过滤。
// 结束时恢复运行前的敏感词与邮件设置（密码字段不回传，保持不变），删除本次大厅测试消息。
// 用法：source secrets/test.env && node hall_filter_api_test.mjs
import { randomUUID } from 'node:crypto';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const BASE = process.env.CS_BASE || 'http://127.0.0.1:18080';
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(BASE)) { console.error('只允许连本机测试后端'); process.exit(2); }
const SINK_PORT = Number(process.env.HALL_E2E_SMTP_PORT || 2525);
const SINK_DIR = join(process.env.HOME || '.', 'cs-testenv', 'run', 'smtp-sink');
const PASS = process.env.ADMIN_BOOTSTRAP_PASSWORD;
if (!PASS) { console.log('FAIL 缺少环境变量 ADMIN_BOOTSTRAP_PASSWORD'); process.exit(2); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let passed = 0, failed = 0;
const redact = (v) => JSON.stringify(v === undefined ? null : v, (k, x) => (/token|password|secret|authorization/i.test(k) && k !== 'password_set' ? '[REDACTED]' : x)).slice(0, 400);
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + redact(detail)); } };
async function api(method, path, { token, body } = {}) {
  const headers = {};
  if (token) headers.Authorization = 'Bearer ' + token;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let j; try { j = await r.json(); } catch { j = {}; }
  return { status: r.status, ...j };
}
function collect(ws) { const ev = []; ws.onmessage = (m) => { try { ev.push(JSON.parse(m.data)); } catch {} }; return ev; }
async function waitFor(events, pred, ms = 5000) { const end = Date.now() + ms; while (Date.now() < end) { const f = events.find(pred); if (f) return f; await sleep(60); } return null; }
const opened = (ws) => new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('ws error')); setTimeout(() => rej(new Error('ws timeout')), 6000); });
const sinkFiles = () => { try { return readdirSync(SINK_DIR).filter((f) => f.endsWith('.eml')).map((f) => join(SINK_DIR, f)); } catch { return []; } };
function decodeMail(file) {
  const raw = readFileSync(file, 'utf8');
  const [head, ...rest] = raw.split('\r\n\r\n');
  const subj = (head.match(/^Subject: (.*)$/m) || [])[1] || '';
  const subject = subj.replace(/=\?UTF-8\?[bB]\?([^?]+)\?=/g, (_, b) => Buffer.from(b, 'base64').toString('utf8')).replace(/\s+/g, '');
  const body = Buffer.from(rest.join('\r\n\r\n').replace(/\s+/g, ''), 'base64').toString('utf8');
  return { head, subject, body };
}
async function waitMail(before, pred, ms) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    for (const f of sinkFiles().filter((f) => !before.includes(f)).sort((a, b) => statSync(a).mtimeMs - statSync(b).mtimeMs)) {
      const m = decodeMail(f);
      if (pred(m)) return m;
    }
    await sleep(500);
  }
  return null;
}

const login = await api('POST', '/api/agent/login', { body: { username: process.env.QA_ADMIN_USER || 'qa_admin', password: PASS } });
if (!login.token) { console.log('FAIL 管理员登录失败'); process.exit(2); }
const T = login.token;
const msgIds = [];
let origFilter = null, origMail = null, vws = null, aws = null;
try {
  // 1. 未登录 / 默认值
  check('未登录读取敏感词设置 401', (await api('GET', '/api/admin/hall/filter')).status === 401);
  const f0 = await api('GET', '/api/admin/hall/filter', { token: T });
  origFilter = f0.data;
  check('读取敏感词设置：结构完整（类别、内置词库、上限）', f0.code === 0 && Array.isArray(f0.data.categories) && f0.data.builtin && f0.data.builtin.gambling.length > 0 && f0.data.limits.custom_words === 500, f0);
  const m0 = await api('GET', '/api/admin/hall/alert-mail', { token: T });
  origMail = m0.data;
  check('读取邮件设置：不含密码字段', m0.code === 0 && !Object.keys(m0.data).some((k) => /password/i.test(k) && k !== 'password_set'), m0);

  // 2. 保存校验与规范化
  const base = { enabled: true, mode: 'mask', categories: ['ad', 'link', 'porn', 'gambling', 'contact'], custom_words: [], allow_words: [], allow_domains: [] };
  let r = await api('POST', '/api/admin/hall/filter', { token: T, body: { ...base, mode: 'delete' } });
  check('非法处理方式 400/40062', r.status === 400 && r.code === 40062, r);
  r = await api('POST', '/api/admin/hall/filter', { token: T, body: { ...base, custom_words: ['，。！'] } });
  check('只有符号的自定义词 400/40062', r.status === 400 && r.code === 40062, r);
  r = await api('POST', '/api/admin/hall/filter', { token: T, body: { ...base, custom_words: ['竞品QA', ' 竞 品 qa '], allow_words: ['进群须知'], allow_domains: ['https://卖号.com/x'] } });
  check('保存成功：自定义词去重、中文域名转 punycode', r.code === 0 && r.data.custom_words.length === 1 && r.data.allow_domains[0] === 'xn--5kroo.com', r);
  r = await api('POST', '/api/admin/hall/filter/test', { token: T, body: { content: '推荐竞品QA，也可以看进群须知' } });
  check('检测试用：自定义词命中、放行词不命中', r.code === 0 && r.data.filtered && r.data.categories.includes('custom') && r.data.masked.includes('进群须知'), r);
  r = await api('POST', '/api/admin/hall/filter/test', { token: T, body: { content: '加我微信 abc123456', config: { ...base, mode: 'whole' } } });
  check('检测试用：保存前预览（整条替换）', r.code === 0 && r.data.masked === '**', r);

  // 3. 邮件设置与测试邮件（本机接收器）
  r = await api('POST', '/api/admin/hall/alert-mail', { token: T, body: { enabled: true, to: ['qa@example.test'], from: 'kelili-test@example.test', smtp_host: 'smtp.163.com', smtp_port: 25, smtp_security: 'none', smtp_username: 'qa' } });
  check('明文发往外网邮件服务器 400/40063', r.status === 400 && r.code === 40063, r);
  r = await api('POST', '/api/admin/hall/alert-mail', { token: T, body: { enabled: true, to: ['qa@example.test'], from: 'kelili-test@example.test', smtp_host: '127.0.0.1', smtp_port: SINK_PORT, smtp_security: 'none', smtp_username: 'qa', smtp_password: 'qa-pass' } });
  check('保存邮件设置：已配置、密码已设置', r.code === 0 && r.data.configured && r.data.password_set && !('smtp_password' in r.data), r);
  let before = sinkFiles();
  r = await api('POST', '/api/admin/hall/alert-mail/test', { token: T });
  const testMail = await waitMail(before, (m) => m.subject.includes('测试邮件'), 8000);
  check('测试邮件：接口成功且接收器收到', r.code === 0 && !!testMail && testMail.head.includes('qa@example.test'), { r, got: !!testMail });

  // 4. 访客发敏感内容 → 屏蔽；客服订阅看到屏蔽后内容
  const s = await api('POST', '/api/visitor/session', { body: { site_id: 'default', identifier: 'QA敏感词', visitor_id: '', last_page: 'https://example.test/filter', ua: 'QA-Hall-Filter/1.0' } });
  vws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/visitor', ['cs-auth', s.visitor_token]);
  const ve = collect(vws); await opened(vws); await waitFor(ve, (e) => e.type === 'hello');
  aws = new WebSocket(BASE.replace(/^http/, 'ws') + '/ws/agent', ['cs-auth', T]);
  const ae = collect(aws); await opened(aws);
  for (const [ws, ev] of [[vws, ve], [aws, ae]]) { const id = randomUUID(); ws.send(JSON.stringify({ type: 'hall_subscribe', id })); await waitFor(ev, (e) => e.id === id && e.type === 'hall_subscribed'); }
  before = sinkFiles();
  const secret = '加我微信 abc123456 看片，上 www.spam-site.top';
  const id = randomUUID(); msgIds.push(id);
  vws.send(JSON.stringify({ type: 'hall_chat', id, content: secret, ts: Date.now() }));
  const ack = await waitFor(ve, (e) => e.type === 'hall_ack' && e.id === id);
  check('访客回执带屏蔽后内容与 filtered 标记', ack && ack.extra && ack.extra.filtered === true && !ack.content.includes('abc123456') && ack.content.includes('**'), ack);
  const seen = await waitFor(ae, (e) => e.type === 'hall_chat' && e.id === id);
  check('客服收到的广播是屏蔽后内容（无微信号、网址、色情词）', seen && !/abc123456|spam-site|看片/.test(seen.content), seen);
  const hist = await api('GET', '/api/agent/hall/messages?limit=5', { token: T });
  const h = (hist.data || []).find((m) => m.id === id);
  check('历史记录为屏蔽后内容', h && !h.content.includes('abc123456'), h);
  const ev = await api('GET', '/api/admin/hall/filter/events?limit=5', { token: T });
  const rec = (ev.data || []).find((x) => x.original === secret);
  check('拦截记录：原文、访客编号、类别、北京时间', rec && rec.vid === s.visitor_id && rec.categories.includes('contact') && rec.categories.includes('link') && rec.created_at.endsWith('+08:00'), rec);
  const alert = await waitMail(before, (m) => m.subject.includes('拦截敏感消息') && m.body.includes('abc123456'), 45000);
  check('告警邮件（合并发送）收到原文与北京时间', !!alert && alert.body.includes(s.visitor_id) && /\+08:00/.test(alert.body), alert && alert.subject);
  const ev2 = await api('GET', '/api/admin/hall/filter/events?limit=5', { token: T });
  check('发送后拦截记录状态为 sent', (ev2.data || []).some((x) => x.original === secret && x.mail_status === 'sent'), ev2.data && ev2.data[0]);

  // 5. 正常消息与客服消息不受影响
  await sleep(2200);
  const id2 = randomUUID(); msgIds.push(id2);
  vws.send(JSON.stringify({ type: 'hall_chat', id: id2, content: '可以用微信支付吗？这个号多少钱', ts: Date.now() }));
  const ack2 = await waitFor(ve, (e) => e.type === 'hall_ack' && e.id === id2);
  check('正常访客消息不屏蔽', ack2 && !ack2.content && !(ack2.extra && ack2.extra.filtered), ack2);
  const id3 = randomUUID(); msgIds.push(id3);
  aws.send(JSON.stringify({ type: 'hall_chat', id: id3, content: '官方客服微信 kefu12345', ts: Date.now() }));
  const ack3 = await waitFor(ae, (e) => e.type === 'hall_ack' && e.id === id3);
  check('客服消息不过滤', ack3 && !ack3.content, ack3);
} catch (e) {
  check('脚本执行无异常', false, String(e && e.stack || e).slice(0, 300));
} finally {
  // 恢复设置、删除测试消息
  if (origFilter) {
    const r = await api('POST', '/api/admin/hall/filter', { token: T, body: { enabled: origFilter.enabled, mode: origFilter.mode, categories: origFilter.categories, custom_words: origFilter.custom_words, allow_words: origFilter.allow_words, allow_domains: origFilter.allow_domains } });
    check('恢复运行前的敏感词设置', r.code === 0, r);
  }
  if (origMail && origMail.configured) {
    const r = await api('POST', '/api/admin/hall/alert-mail', { token: T, body: { enabled: origMail.enabled, to: origMail.to, from: origMail.from, smtp_host: origMail.smtp_host, smtp_port: origMail.smtp_port, smtp_security: origMail.smtp_security, smtp_username: origMail.smtp_username } });
    check('恢复运行前的邮件设置（密码不回传）', r.code === 0, r);
  }
  for (const id of msgIds) await api('POST', '/api/agent/hall/messages/' + id + '/delete', { token: T });
  for (const ws of [vws, aws]) { try { ws && ws.close(); } catch {} }
  console.log('SUMMARY passed=' + passed + ' failed=' + failed);
  process.exit(failed ? 1 : 0);
}
