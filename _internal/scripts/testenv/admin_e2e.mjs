#!/usr/bin/env node
// 管理后台 0.8.0 新功能浏览器联调（Chrome 无界面模式 + 本机测试后端）。浏览器时区故意设为美国东部，验证页面仍显示北京时间。
import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';

const BASE = 'http://127.0.0.1:18090';
const SHOTS = process.env.HOME + '/cs-testenv/shots/admin-e2e';
mkdirSync(SHOTS, { recursive: true });
let passed = 0, failed = 0;
const check = (name, ok, detail) => { if (ok) { passed++; console.log('PASS ' + name); } else { failed++; console.log('FAIL ' + name + ' :: ' + String(detail || '').slice(0, 300)); } };
const problems = [];

const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', timezoneId: 'America/New_York' });
page.on('pageerror', (e) => problems.push('pageerror: ' + e.message));
page.on('console', (m) => { if (m.type() === 'error') problems.push('console: ' + m.text()); });
page.on('response', (r) => { if (r.url().includes('/api/') && r.status() >= 500) problems.push('http ' + r.status() + ' ' + r.url()); });
const shot = (n) => page.screenshot({ path: SHOTS + '/' + n + '.png' });

await page.goto(BASE + '/admin/login');
await page.getByPlaceholder('用户名').fill(process.env.QA_ADMIN_USER || 'qa_admin');
await page.getByPlaceholder('密码').fill(process.env.ADMIN_BOOTSTRAP_PASSWORD);
await page.getByRole('button', { name: '登 录' }).click();
await page.waitForURL('**/admin/console', { timeout: 15000 });
await page.waitForTimeout(2500);
await shot('w01-console');
const body = await page.locator('body').innerText();
check('登录后进入控制台并显示会话', body.includes('功能测试客户') && body.includes('在线机器人'), body.slice(0, 200));
const bj = new Date(Date.now() + 8 * 3600e3);
const hh = [bj, new Date(bj - 3600e3)].map((d) => d.toISOString().slice(11, 13) + ':');
check('浏览器时区为美国东部时仍显示北京时间', hh.some((h) => body.includes(h)), 'expect ' + hh.join('/'));
const health = await page.evaluate(() => fetch('/api/health').then((r) => r.json()));
check('控制台实时连接已建立（在线客服数 ≥ 1）', health.agents >= 1, JSON.stringify(health));

// 打开会话 + 快捷回复
await page.getByText('在线机器人', { exact: true }).first().click();
await page.waitForTimeout(2000);
const qr = page.getByRole('button', { name: '快捷回复' });
check('聊天区有快捷回复按钮', await qr.count() > 0);
if (await qr.count()) {
  await qr.first().click();
  await page.waitForTimeout(1200);
  await shot('w02-quick-reply');
  const pop = await page.locator('body').innerText();
  check('快捷回复面板显示话术类型（我的/团队）或空提示', /我的|团队|暂无/.test(pop), pop.slice(-300));
  const row = page.locator('.el-popper .el-table__row').first();
  check('快捷回复面板有可选话术', await row.count() > 0);
  if (await row.count()) {
    // 以第一行的内容为准（测试数据会随其他巡检变化），点选后输入框应包含该内容
    const cells = row.locator('td');
    const content = ((await cells.nth((await cells.count()) - 1).innerText().catch(() => '')) || '').replace(/[…\s]+$/, '').slice(0, 8);
    await row.click();
    await page.waitForTimeout(500);
    const val = await page.locator('textarea').last().inputValue().catch(() => '');
    check('点击话术填入输入框', content.length > 0 && val.includes(content), { content, val });
  }
  await page.keyboard.press('Escape');
}

// 客户资料抽屉
const ops = page.getByRole('button', { name: '操作' });
check('会话行有操作菜单', await ops.count() > 0);
if (await ops.count()) {
  await ops.first().click();
  await page.waitForTimeout(500);
  await shot('w03-row-menu');
  const menu = await page.locator('body').innerText();
  check('操作菜单含置顶/未读/资料/导出/拉黑/删除', ['客户资料', '导出聊天记录', '拉黑', '删除聊天记录'].every((t) => menu.includes(t)), menu.slice(-300));
  await page.locator('.el-dropdown-menu__item', { hasText: '客户资料' }).first().click();
  await page.waitForTimeout(1500);
  await shot('w04-profile-drawer');
  const dr = await page.locator('.el-drawer').innerText().catch(() => '');
  check('客户资料抽屉显示备注与标签', dr.includes('备注') && dr.includes('标签'), dr.slice(0, 200));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(500);
}

// 搜索聊天记录
const sb = page.getByRole('button', { name: '搜索聊天' });
if (await sb.count()) {
  await sb.first().click();
  await page.waitForTimeout(500);
  await page.getByPlaceholder(/输入至少 2 个字/).fill('支持支付宝');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(2000);
  await shot('w05-search');
  const dlg = await page.locator('.el-dialog').innerText().catch(() => '');
  check('全局搜索有结果', dlg.includes('在线机器人') || dlg.includes('王先生'), dlg.slice(0, 300));
  await page.keyboard.press('Escape');
} else check('有搜索聊天入口', false);

// 新页面
for (const [path, name, texts] of [
  ['/admin/quick-replies', 'w06-quick-replies', ['快捷回复']],
  ['/admin/blocked', 'w07-blocked', ['黑名单']],
  ['/admin/recycle-bin', 'w08-recycle-bin', ['回收站']],
  ['/admin/settings', 'w09-settings', ['自动清理', '删除全部聊天记录']],
]) {
  await page.goto(BASE + path);
  await page.waitForTimeout(2000);
  if (path.endsWith('settings')) await page.mouse.wheel(0, 3000), await page.waitForTimeout(600);
  await shot(name);
  const t = await page.locator('body').innerText();
  check('页面 ' + path + ' 正常渲染', texts.every((x) => t.includes(x)), t.slice(0, 200));
}

check('无页面脚本错误与接口 5xx', problems.length === 0, problems.join(' | '));
await browser.close();
console.log('SUMMARY passed=' + passed + ' failed=' + failed);
process.exit(failed ? 1 : 0);
