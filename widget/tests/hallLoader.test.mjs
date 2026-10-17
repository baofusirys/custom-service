import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createRequire } from 'node:module'

// [119] loader.js 运行在宿主页，不能引用 iframe 内的 hall-core.js；这里抽取其中的校验代码段单测，
// 并与 hall-core.js 的状态构造规则对照，保证两端规则一致。
const src = readFileSync(new URL('../public/loader.js', import.meta.url), 'utf8')
const BEGIN = '// ====== [119] 大厅状态消息校验 开始'
const END = '// ====== [119] 大厅状态消息校验 结束'
const start = src.indexOf(BEGIN)
const end = src.indexOf(END)
assert.ok(start > 0 && end > start, '找不到 loader.js 大厅校验代码段')
const ctx = { URL }
vm.createContext(ctx)
vm.runInContext(src.slice(start, end) + '\n;this.api = { isTrustedFrameMessage, parseHallStatus, safeLightboxSrc, hallChipText, HALL_STATES };', ctx)
const L = ctx.api
const Core = createRequire(import.meta.url)('../public/hall-core.js')
const FRAME = 'https://cs.example.com'

test('isTrustedFrameMessage：必须同时满足来源窗口、origin 与消息结构', () => {
  const frameWin = {}
  const ok = { source: frameWin, origin: FRAME, data: { __cs: 1, type: 'hall_status' } }
  assert.equal(L.isTrustedFrameMessage(ok, frameWin, FRAME), true)
  assert.equal(L.isTrustedFrameMessage({ ...ok, source: {} }, frameWin, FRAME), false, '其他窗口（宿主页自己的 iframe）伪造')
  assert.equal(L.isTrustedFrameMessage({ ...ok, origin: 'https://evil.example' }, frameWin, FRAME), false, '其他 origin')
  assert.equal(L.isTrustedFrameMessage({ ...ok, origin: 'https://cs.example.com.evil.example' }, frameWin, FRAME), false, '前缀相同的域名')
  assert.equal(L.isTrustedFrameMessage({ ...ok, data: '{"__cs":1}' }, frameWin, FRAME), false, '字符串消息')
  assert.equal(L.isTrustedFrameMessage({ ...ok, data: { __cs: '1', type: 'close' } }, frameWin, FRAME), false)
  assert.equal(L.isTrustedFrameMessage({ ...ok, data: { __cs: 1 } }, frameWin, FRAME), false, '缺少 type')
  assert.equal(L.isTrustedFrameMessage(ok, null, FRAME), false, 'iframe 尚未加载')
})

test('parseHallStatus：白名单状态、在线人数收敛、speaking 只认布尔 true', () => {
  assert.deepEqual({ ...L.parseHallStatus({ __cs: 1, type: 'hall_status', state: 'live', online: 12, speaking: true }) }, { state: 'live', online: 12, speaking: true })
  assert.deepEqual({ ...L.parseHallStatus({ __cs: 1, type: 'hall_status', state: 'blocked', online: 1e9, speaking: 'yes' }) }, { state: 'blocked', online: 100000, speaking: false })
  assert.deepEqual({ ...L.parseHallStatus({ __cs: 1, type: 'hall_status', state: 'muted', online: -3 }) }, { state: 'muted', online: 0, speaking: false })
  assert.equal(L.parseHallStatus({ __cs: 1, type: 'hall_status', state: '__proto__', online: 1 }), null)
  assert.equal(L.parseHallStatus({ __cs: 1, type: 'hall_status', state: 'toString', online: 1 }), null)
  assert.equal(L.parseHallStatus({ __cs: 1, type: 'hall_status', state: '<b>x</b>' }), null)
  assert.equal(L.parseHallStatus({ __cs: 1, type: 'unread', state: 'live' }), null)
  assert.equal(L.parseHallStatus(null), null)
})

test('loader 与 hall-core 的状态规则一致：hall-core 构造的每种状态 loader 都能解析', () => {
  for (const state of Object.keys(L.HALL_STATES)) {
    const msg = Core.buildHallStatus({ state, online: 3, speaking: true })
    const st = L.parseHallStatus(msg)
    assert.ok(st, state)
    assert.equal(st.state, state)
  }
  // hall-core 对未知状态一律降级为 off，loader 对 off 不显示胶囊
  assert.equal(L.hallChipText(L.parseHallStatus(Core.buildHallStatus({ state: 'whatever' }))), null)
})

test('hallChipText：中文文案（宽屏 / 窄屏）', () => {
  assert.equal(L.hallChipText({ state: 'live', online: 8, speaking: false }).full, '大厅 8 人 · 收听中')
  assert.equal(L.hallChipText({ state: 'live', online: 8, speaking: true }).full, '大厅 8 人 · 正在播放')
  assert.equal(L.hallChipText({ state: 'live', online: 8 }).compact, '8 人')
  assert.equal(L.hallChipText({ state: 'blocked', online: 8 }).full, '点击收听大厅')
  assert.equal(L.hallChipText({ state: 'muted', online: 2 }).full, '大厅 2 人 · 已静音')
  assert.equal(L.hallChipText({ state: 'elsewhere' }).full, '已在其他页面收听')
  assert.equal(L.hallChipText({ state: 'kicked' }).full, '已被移出大厅')
  assert.equal(L.hallChipText({ state: 'off' }), null)
})

test('safeLightboxSrc：大图只允许挂件同源的 /files/ 地址', () => {
  assert.equal(L.safeLightboxSrc(FRAME + '/files/2026/10/17/a.png?access=x', FRAME), FRAME + '/files/2026/10/17/a.png?access=x')
  assert.equal(L.safeLightboxSrc('https://evil.example/files/a.png', FRAME), '')
  assert.equal(L.safeLightboxSrc('javascript:alert(1)', FRAME), '')
  assert.equal(L.safeLightboxSrc('data:image/svg+xml,<svg onload=alert(1)>', FRAME), '')
  assert.equal(L.safeLightboxSrc(FRAME + '/api/agent/export', FRAME), '')
  assert.equal(L.safeLightboxSrc('/files/a.png', FRAME), '', '相对地址无法确认来源，拒绝')
  assert.equal(L.safeLightboxSrc(123, FRAME), '')
})

test('loader 消息处理入口先做可信来源校验，postMessage 不再使用 * 目标', () => {
  assert.match(src, /if \(!isTrustedFrameMessage\(ev, iframe\.contentWindow, frameOrigin\)\) return;/)
  assert.doesNotMatch(src, /contentWindow\.postMessage\([^)]*'\*'\)/)
  assert.match(src, /postMessage\(msg, frameOrigin\)/)
  assert.match(src, /var lbSrc = safeLightboxSrc\(ev\.data\.src, frameOrigin\);/)
  assert.doesNotThrow(() => new vm.Script(src))
})
