#!/usr/bin/env node
// 新功能 UI/集成测试前的数据复位（只连本机隔离测试后端）：恢复、解除拉黑、取消置顶、清空资料，并让访客各发一条新消息。
// 用法（Mac 测试环境）：set -a; source secrets/test.env; set +a; node seed_features.mjs
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';

const BASE = 'http://127.0.0.1:18080';
const HOME = process.env.HOME;
const names = { '待删除甲': 'del1', '待删除乙': 'del2', '功能测试客户': 'feat', '集成待删除': 'del3', '待结束': 'toend' };
const login = await (await fetch(BASE + '/api/agent/login', { method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username: process.env.QA_ADMIN_USER || 'qa_admin', password: process.env.ADMIN_BOOTSTRAP_PASSWORD }) })).json();
if (!login.token) { console.error('login failed'); process.exit(1); }
const post = async (path, body) => {
  const r = await fetch(BASE + path, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + login.token }, body: JSON.stringify(body) });
  return (await r.json()).code;
};
// 清理历次 UI 测试创建的同名快捷回复
const qr = await (await fetch(BASE + '/api/agent/quick-replies', { headers: { Authorization: 'Bearer ' + login.token } })).json();
for (const q of (qr.data || []).filter((x) => x.scope === 'personal' && (x.title === '付款方式' || String(x.title || '').startsWith('F巡检')))) await post('/api/agent/quick-replies/' + q.id + '/delete', {});
// 后台浏览器巡检需要至少一条团队话术（快捷回复面板点选填入）；不存在时补一条固定的
if (!(qr.data || []).some((x) => x.scope === 'team' && x.title === '巡检团队话术')) await post('/api/agent/quick-replies', { scope: 'team', title: '巡检团队话术', content: '您好，这里是团队话术示例', sort_order: 99 });
for (const [name, file] of Object.entries(names)) {
  const vf = HOME + '/cs-testenv/vids/' + file;
  if (existsSync(vf)) {
    const vid = readFileSync(vf, 'utf8').trim();
    await post('/api/admin/recycle-bin/restore', { visitor_ids: [vid] });
    await post('/api/agent/visitor/' + vid + '/block', { blocked: false });
    await post('/api/agent/visitor/' + vid + '/pin', { pinned: false });
    await post('/api/agent/visitor/' + vid + '/unread', { unread: false });
    await post('/api/agent/visitor/' + vid + '/profile', { note: '', tags: [] });
  }
  execFileSync('node', [HOME + '/cs-testenv/visitor.mjs', '--name', name, '--say', name + ' 的测试消息', '--vidfile', vf, '--stay', '2'], { stdio: 'ignore' });
  console.log('seeded ' + name);
}
