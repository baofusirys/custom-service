import test from 'node:test'
import assert from 'node:assert/strict'

import { bootWidget } from './helpers/fakeWidgetEnv.mjs'

const CACHE_KEY = 'cs_visitor_default_msgs'
const RESET_KEY = 'cs_visitor_default_history_reset_at'
const BASE = Date.parse('2026-10-14T12:00:00Z')

function agentMsg(id, content, sec = 0, extra = {}) {
  return { id, conv_id: 'conv-0', sender: 'agent', content, media_url: '', media_kind: '', media_name: '', created_at: new Date(BASE - 3600 * 1000 + sec * 1000).toISOString(), ...extra }
}
function agentChat(id, content, ts = BASE) {
  return { type: 'chat', from: 'agent:7', id, conv: 'conv-1', content, ts }
}
const cacheOf = (env) => JSON.parse(env.localStorage.getItem(CACHE_KEY) || 'null')

test('history_cleared：清空聊天区与本地缓存、重置去重状态并显示系统提示', async () => {
  const env = await bootWidget({ storage: { [CACHE_KEY]: JSON.stringify([agentMsg('m2-a', '旧消息一'), agentMsg('m2-b', '旧消息二', 10)]) } })
  assert.match(env.listText(), /旧消息一/)
  const ws = env.ws()
  ws.open()
  ws.receive(agentChat('m2-c', '清空前的新消息'))
  assert.ok(env.posted.some(p => p.type === 'unread' && p.count === 1))

  ws.receive({ type: 'history_cleared', ts: BASE + 500 })
  assert.equal(env.list.children.length, 1)
  assert.equal(env.list.children[0].className, 'sys-chip')
  assert.equal(env.listText(), '聊天记录已清空')
  assert.equal(env.localStorage.getItem(CACHE_KEY), null)
  assert.deepEqual(JSON.parse(env.localStorage.getItem(RESET_KEY)), { at: BASE + 500, live: true })
  assert.equal(env.posted.at(-1).type, 'unread')
  assert.equal(env.posted.at(-1).count, 0)

  // seenMessageIDs 已清空：之后到达的消息（即使 ID 与旧消息相同）正常显示
  ws.receive(agentChat('m2-a', '清空后的新消息', BASE + 1000))
  assert.match(env.listText(), /清空后的新消息/)
  assert.doesNotMatch(env.listText(), /旧消息/)
  assert.deepEqual(cacheOf(env).map(m => m.id), ['m2-a'])
})

test('会话接口 history_reset_at 大于已处理值时先清空本地缓存再渲染', async () => {
  const cached = JSON.stringify([agentMsg('m2-a', '离线前的消息')])
  const fresh = await bootWidget({ storage: { [CACHE_KEY]: cached }, session: { history_reset_at: BASE + 1000 } })
  assert.doesNotMatch(fresh.listText(), /离线前的消息/)
  assert.equal(fresh.localStorage.getItem(CACHE_KEY), null)
  assert.deepEqual(JSON.parse(fresh.localStorage.getItem(RESET_KEY)), { at: BASE + 1000, live: false })

  const same = await bootWidget({ storage: { [CACHE_KEY]: cached, [RESET_KEY]: JSON.stringify({ at: BASE + 1000, live: false }) }, session: { history_reset_at: BASE + 1000 } })
  assert.match(same.listText(), /离线前的消息/)

  const never = await bootWidget({ storage: { [CACHE_KEY]: cached }, session: { history_reset_at: 0 } })
  assert.match(never.listText(), /离线前的消息/)
  assert.equal(never.localStorage.getItem(RESET_KEY), null)
})

test('在线清空后服务端记录时间仅有毫秒差：视为同一次清空，保留清空后的新消息', async () => {
  const cached = JSON.stringify([agentMsg('m2-n', '清空后聊的内容')])
  const sameEvent = await bootWidget({ storage: { [CACHE_KEY]: cached, [RESET_KEY]: JSON.stringify({ at: BASE, live: true }) }, session: { history_reset_at: BASE + 5 } })
  assert.match(sameEvent.listText(), /清空后聊的内容/)
  assert.deepEqual(JSON.parse(sameEvent.localStorage.getItem(RESET_KEY)), { at: BASE + 5, live: false })

  const laterDelete = await bootWidget({ storage: { [CACHE_KEY]: cached, [RESET_KEY]: JSON.stringify({ at: BASE, live: true }) }, session: { history_reset_at: BASE + 120000 } })
  assert.doesNotMatch(laterDelete.listText(), /清空后聊的内容/)
  assert.equal(laterDelete.localStorage.getItem(CACHE_KEY), null)
})

test('recalled_ids 把本地缓存改写为已撤回，渲染占位且不泄露正文与附件', async () => {
  const env = await bootWidget({
    storage: {
      [CACHE_KEY]: JSON.stringify([
        agentMsg('m2-x', '机密内容'),
        agentMsg('m2-img', '', 5, { media_url: '/files/a.png', media_kind: 'image', media_name: 'a.png' }),
        agentMsg('m2-y', '保留内容', 10),
      ]),
    },
    session: { recalled_ids: ['m2-x', 'm2-img', 'm2-z', '../bad', 123, null] },
  })
  const cache = cacheOf(env)
  const x = cache.find(m => m.id === 'm2-x')
  assert.equal(x.recalled, true)
  assert.equal(x.content, '')
  const img = cache.find(m => m.id === 'm2-img')
  assert.equal(img.recalled, true)
  assert.equal(img.media_url, '')
  assert.equal(img.media_name, '')
  assert.equal(cache.find(m => m.id === 'm2-y').content, '保留内容')
  assert.doesNotMatch(env.listText(), /机密内容/)
  assert.equal(env.byClass('recall-tip').length, 2)
  assert.equal(env.byClass('img-msg').length, 0)
  assert.match(env.listText(), /客服撤回了一条消息/)
  assert.match(env.listText(), /保留内容/)

  // 不在缓存里的已撤回消息被服务端重放（仍带正文）：只显示占位，缓存也只写入占位
  const ws = env.ws()
  ws.open()
  ws.receive(agentChat('m2-z', '重放的已撤回正文'))
  assert.doesNotMatch(env.listText(), /重放的已撤回正文/)
  const z = cacheOf(env).find(m => m.id === 'm2-z')
  assert.equal(z.recalled, true)
  assert.equal(z.content, '')
})

test('recall 事件：气泡替换为占位、更新缓存，整组撤回后不留空头像，重放不复活', async () => {
  const env = await bootWidget()
  const ws = env.ws()
  ws.open()
  ws.receive(agentChat('m2-r', '第一条'))
  ws.receive(agentChat('m2-s', '第二条', BASE + 1000))
  assert.equal(env.byMsgId('m2-r')[0].className, 'bubble')
  assert.equal(env.byClass('msg-line').length, 1)

  ws.receive({ type: 'recall', id: 'm2-r', conv: 'conv-1', from: 'agent:7', ts: BASE + 2000 })
  assert.doesNotMatch(env.listText(), /第一条/)
  assert.match(env.listText(), /第二条/)
  const tip = env.byMsgId('m2-r')[0]
  assert.equal(tip.className, 'recall-tip')
  assert.equal(tip.textContent, '客服撤回了一条消息')
  assert.equal(cacheOf(env).find(m => m.id === 'm2-r').recalled, true)
  assert.equal(cacheOf(env).find(m => m.id === 'm2-r').content, '')

  ws.receive({ type: 'recall', id: 'm2-s', conv: 'conv-1', from: 'agent:7', ts: BASE + 3000 })
  assert.equal(env.byClass('msg-line').length, 0)
  const tips = env.byClass('recall-tip')
  assert.equal(tips.length, 2)
  assert.ok(tips.every(t => t.parentNode === env.list))

  ws.receive(agentChat('m2-r', '第一条'))
  assert.doesNotMatch(env.listText(), /第一条/)
  // 非法 ID 直接忽略，不抛错、不改动界面
  const before = env.listText()
  ws.receive({ type: 'recall', id: '"><img src=x>', from: 'agent:7' })
  ws.receive({ type: 'recall', id: 12345, from: 'agent:7' })
  assert.equal(env.listText(), before)
})

test('访客输入 typing 节流：未连接不发，连接后每 3 秒最多一次，空白内容不发', async () => {
  const env = await bootWidget()
  const ws = env.ws()
  const typings = () => ws.sent.filter(e => e.type === 'typing')
  env.input.value = '你'
  env.input.dispatch('input')
  assert.equal(typings().length, 0)

  ws.open()
  env.input.value = '你好'
  env.input.dispatch('input')
  assert.equal(typings().length, 1)
  assert.deepEqual(Object.keys(typings()[0]).sort(), ['ts', 'type'])
  assert.equal(typings()[0].ts, env.clock.now)
  env.clock.tick(1000)
  env.input.dispatch('input')
  env.clock.tick(1999)
  env.input.dispatch('input')
  assert.equal(typings().length, 1)
  env.clock.tick(1)
  env.input.dispatch('input')
  assert.equal(typings().length, 2)

  env.clock.tick(5000)
  env.input.value = '   '
  env.input.dispatch('input')
  assert.equal(typings().length, 2)

  ws.close()
  env.clock.tick(5000)
  env.input.value = '断线中'
  env.input.dispatch('input')
  assert.equal(typings().length, 2)
})

test('收到客服 typing 显示“客服正在输入…”，5 秒无新 typing 或收到客服消息后隐藏', async () => {
  const env = await bootWidget()
  const ws = env.ws()
  ws.open()
  ws.receive({ type: 'typing', from: 'visitor:v-test', conv: 'conv-1' })
  assert.equal(env.byClass('typing-tip').length, 0)

  ws.receive({ type: 'typing', from: 'agent:7', conv: 'conv-1' })
  const tip = env.byClass('typing-tip')[0]
  assert.equal(tip.textContent, '客服正在输入…')
  assert.equal(tip.style.display, '')
  assert.equal(tip.parentNode, env.app)
  assert.equal(env.list.nextSibling, tip)

  env.clock.tick(4000)
  ws.receive({ type: 'typing', from: 'agent:7', conv: 'conv-1' })
  env.clock.tick(4000)
  assert.equal(tip.style.display, '')
  env.clock.tick(1001)
  assert.equal(tip.style.display, 'none')

  ws.receive({ type: 'typing', from: 'agent:7', conv: 'conv-1' })
  assert.equal(tip.style.display, '')
  ws.receive(agentChat('m2-t', '客服回复了'))
  assert.equal(tip.style.display, 'none')

  // 清空聊天记录不会删除提示元素本身
  ws.receive({ type: 'history_cleared', ts: BASE })
  assert.equal(env.byClass('typing-tip').length, 1)
})

test('原有重连、去重与未读逻辑不受影响：重复消息不重复渲染与累计未读，但仍回送达', async () => {
  const env = await bootWidget()
  const ws = env.ws()
  ws.open()
  ws.receive(agentChat('m2-d', '只显示一次'))
  ws.receive(agentChat('m2-d', '只显示一次'))
  assert.equal(env.byMsgId('m2-d').length, 1)
  const unreadPosts = env.posted.filter(p => p.type === 'unread')
  assert.equal(unreadPosts.at(-1).count, 1)
  assert.equal(ws.sent.filter(e => e.type === 'delivery' && e.id === 'm2-d').length, 2)
  // 断线后按退避重连会创建新连接
  ws.close()
  env.clock.tick(1000)
  assert.equal(env.sockets.length, 2)
})
