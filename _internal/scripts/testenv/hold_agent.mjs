#!/usr/bin/env node
// [117] 端到端测试辅助：让一个「其他客服」在线接待指定客户（模拟生产中他人接待的会话），保持连接 N 秒后退出。
// 只连本机测试后端。用法：node hold_agent.mjs --visitor 待结束 --seconds 600
import { randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const BASE = 'http://127.0.0.1:18080';
const args = Object.fromEntries(process.argv.slice(2).reduce((a, v, i, arr) => (v.startsWith('--') ? [...a, [v.slice(2), arr[i + 1]]] : a), []));
const NAME = args.visitor || '待结束';
const SECONDS = Math.min(Math.max(Number(args.seconds) || 600, 10), 3600);
const MYSQL = '/opt/homebrew/opt/mysql@8.4/bin/mysql';
const sql = (q) => execFileSync(MYSQL, ['--socket=' + process.env.HOME + '/cs-testenv/run/mysql.sock', '-uroot', '-N', 'cs_test', '-e', q],
  { env: { ...process.env, MYSQL_PWD: process.env.MYSQL_ROOT_PASSWORD }, encoding: 'utf8' }).trim();
const api = async (method, path, token, body) => {
  const r = await fetch(BASE + path, { method, headers: { ...(token ? { Authorization: 'Bearer ' + token } : {}), ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  return { status: r.status, ...(await r.json().catch(() => ({}))) };
};
const admin = await api('POST', '/api/agent/login', null, { username: process.env.QA_ADMIN_USER || 'qa_admin', password: process.env.ADMIN_BOOTSTRAP_PASSWORD });
// 每次新建一个值班客服（测试库），停用以前留下的，避免依赖重置密码接口
sql("UPDATE agents SET active=0 WHERE username LIKE 'qa_hold_%'");
const user = 'qa_hold_' + randomUUID().slice(0, 6), nick = '值班小李';
const pass = 'Hd!' + randomUUID().slice(0, 10) + 'Zx9#';
const created = await api('POST', '/api/admin/agents', admin.token, { username: user, password: pass, role: 'agent', nickname: nick });
if (created.code !== 0) { console.error('create holder failed', created.status, created.code); process.exit(1); }
const l = await api('POST', '/api/agent/login', null, { username: user, password: pass });
if (!l.token) { console.error('holder login failed', l.status, l.code); process.exit(1); }
const ws = new WebSocket(BASE.replace('http', 'ws') + '/ws/agent', ['cs-auth', l.token]);
await new Promise((r) => (ws.onopen = r));
const conv = sql("SELECT c.id FROM conversations c JOIN visitors v ON v.id=c.visitor_id WHERE v.identifier='" + NAME.replace(/'/g, "''") + "' AND c.deleted_at IS NULL ORDER BY (c.status='open') DESC, c.updated_at DESC LIMIT 1");
// 直接把会话分配给值班客服（测试库），模拟「他人正在接待」
sql("UPDATE conversations SET agent_id=" + Number(l.agent.id) + " WHERE id='" + conv + "'");
// 可选：再建一个普通客服供 App 端以「非管理员」身份巡检，凭据写入只有本人可读的文件（不打印）
if (args['agent-file']) {
  sql("UPDATE agents SET active=0 WHERE username LIKE 'qa_e2e_%'");
  const eu = 'qa_e2e_' + randomUUID().slice(0, 6), ep = 'Ee!' + randomUUID().slice(0, 10) + 'Zx9#';
  const er = await api('POST', '/api/admin/agents', admin.token, { username: eu, password: ep, role: 'agent', nickname: '巡检客服' });
  if (er.code !== 0) { console.error('create e2e agent failed', er.status, er.code); process.exit(1); }
  const { writeFileSync } = await import('node:fs');
  writeFileSync(args['agent-file'], JSON.stringify({ user: eu, pass: ep }), { mode: 0o600 });
}
console.log('holding', NAME, conv.slice(0, 8), 'as', user, 'for', SECONDS, 's');
setInterval(() => ws.send(JSON.stringify({ type: 'ping', ts: Date.now() })), 20000);
setTimeout(() => { ws.close(); process.exit(0); }, SECONDS * 1000);
